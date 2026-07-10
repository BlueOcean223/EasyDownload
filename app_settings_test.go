package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"EasyDownload/internal/api"
	"EasyDownload/internal/config"
	"EasyDownload/internal/download"
	downloadtask "EasyDownload/internal/download/task"
	"EasyDownload/internal/settings"

	"github.com/stretchr/testify/require"
)

func TestAppSettingsPlannerClassifiesEveryRuntimeCategory(t *testing.T) {
	current := settings.FromConfig(config.DefaultConfig())
	proxyRuntime := &fakeSettingsProxyRuntime{port: current.ProxyPort}
	planner := appSettingsEffectPlanner{app: &App{}, proxyRuntime: proxyRuntime}
	candidate := current
	candidate.DownloadDir = filepath.Join(t.TempDir(), "downloads")
	candidate.MaxConcurrent = current.MaxConcurrent + 1
	candidate.UpstreamProxy = "http://127.0.0.1:7890"
	candidate.UseUpstreamProxy = true
	candidate.ProxyDebug = !current.ProxyDebug
	candidate.ProxyPort++
	candidate.APIPort++

	plan, err := planner.Plan(current, candidate, settings.Diff(current, candidate))
	require.NoError(t, err)
	require.Len(t, plan.CriticalReversible, 4)
	require.Len(t, plan.DeferredRestart, 1)
	require.Len(t, plan.BestEffort, 1)
	require.Equal(t, "download_runtime_config", plan.CriticalReversible[0].Name())
	require.Equal(t, "proxy_port", plan.CriticalReversible[3].Name())
	require.Equal(t, []string{"apiPort"}, plan.DeferredRestart[0].Requirement().Fields)
}

func TestAppSettingsPlannerPureFieldNeedsNoFieldSpecificEffect(t *testing.T) {
	planner := appSettingsEffectPlanner{app: &App{}}
	current := settings.FromConfig(config.DefaultConfig())
	candidate := current
	candidate.FirstRunComplete = !current.FirstRunComplete

	plan, err := planner.Plan(current, candidate, settings.Diff(current, candidate))
	require.NoError(t, err)
	require.Empty(t, plan.CriticalReversible)
	require.Empty(t, plan.DeferredRestart)
	require.Len(t, plan.BestEffort, 1, "only the common settings-changed publisher is expected")
}

func TestAppSettingsDownloadEffectsApplyAndRollback(t *testing.T) {
	oldDir := filepath.Join(t.TempDir(), "old")
	newDir := filepath.Join(t.TempDir(), "new")
	manager := downloader.NewDownloadManager(oldDir, 2)
	app := &App{downloadManager: manager}
	current := settings.FromConfig(config.DefaultConfig())
	current.DownloadDir = oldDir
	current.MaxConcurrent = 2
	candidate := current
	candidate.DownloadDir = newDir
	candidate.MaxConcurrent = 5

	plan, err := (appSettingsEffectPlanner{app: app}).Plan(current, candidate, settings.Diff(current, candidate))
	require.NoError(t, err)
	require.Len(t, plan.CriticalReversible, 1)
	require.NoDirExists(t, newDir)
	for _, effect := range plan.CriticalReversible {
		require.NoError(t, effect.Preflight(context.Background()))
		require.DirExists(t, newDir)
		require.NoError(t, effect.Apply(context.Background()))
		committed, ok := effect.(settings.CommittedCriticalSettingsEffect)
		require.True(t, ok)
		require.NoError(t, committed.Commit(context.Background()))
	}
	require.Equal(t, newDir, manager.GetDownloadDir())
	require.Equal(t, 5, manager.GetMaxConcurrent())
}

func TestAppSettingsDownloadEffectRollbackRestoresBothFields(t *testing.T) {
	oldDir := filepath.Join(t.TempDir(), "old")
	newDir := filepath.Join(t.TempDir(), "new")
	manager := downloader.NewDownloadManager(oldDir, 2)
	current := settings.FromConfig(config.DefaultConfig())
	current.DownloadDir, current.MaxConcurrent = oldDir, 2
	candidate := current
	candidate.DownloadDir, candidate.MaxConcurrent = newDir, 5
	plan, err := (appSettingsEffectPlanner{app: &App{downloadManager: manager}}).Plan(current, candidate, settings.Diff(current, candidate))
	require.NoError(t, err)
	require.Len(t, plan.CriticalReversible, 1)
	effect := plan.CriticalReversible[0]
	require.NoError(t, effect.Preflight(context.Background()))
	require.NoError(t, effect.Apply(context.Background()))
	require.NoError(t, effect.Rollback(context.Background()))
	require.Equal(t, oldDir, manager.GetDownloadDir())
	require.Equal(t, 2, manager.GetMaxConcurrent())
}

func TestAppSettingsMaxConcurrentPreflightDoesNotProbeDownloadDirectory(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "offline", "downloads")
	manager := downloader.NewDownloadManager(missingDir, 2)
	current := settings.FromConfig(config.DefaultConfig())
	current.DownloadDir, current.MaxConcurrent = missingDir, 2
	candidate := current
	candidate.MaxConcurrent = 3

	plan, err := (appSettingsEffectPlanner{app: &App{downloadManager: manager}}).Plan(current, candidate, settings.Diff(current, candidate))
	require.NoError(t, err)
	require.Len(t, plan.CriticalReversible, 1)
	require.NoError(t, plan.CriticalReversible[0].Preflight(context.Background()))
	require.NoDirExists(t, missingDir)
}

func TestAppSettingsCommitFailureHidesCandidateRuntimeConfigFromCreateTask(t *testing.T) {
	root := t.TempDir()
	persister := &blockingFailurePersister{}
	configManager := config.NewConfigManagerWithPersister(filepath.Join(root, "config.json"), persister)
	require.NoError(t, configManager.Load())
	oldDir := filepath.Join(root, "old")
	initial := configManager.Get()
	initial.DownloadDir = oldDir
	initial.MaxConcurrent = 1
	require.NoError(t, configManager.Commit(context.Background(), initial))

	manager := downloader.NewDownloadManager(oldDir, 1)
	require.NoError(t, manager.RegisterPlatformAdapter(downloader.NewGenericAdapter()))
	app := &App{configManager: configManager, downloadManager: manager}
	app.settingsModule = settings.NewModule(configManager, appSettingsEffectPlanner{app: app})
	persister.armFailure()

	newDir := filepath.Join(root, "candidate")
	newMax := 2
	updateDone := make(chan error, 1)
	go func() {
		_, err := app.UpdateSettings(settings.SettingsPatch{DownloadDir: &newDir, MaxConcurrent: &newMax})
		updateDone <- err
	}()
	<-persister.started

	platformData, err := downloader.MarshalGenericPlatformData("https://example.com/video.mp4", nil)
	require.NoError(t, err)
	created := make(chan *downloader.DownloadTask, 1)
	createErr := make(chan error, 1)
	go func() {
		task, err := manager.CreateTask(downloader.TaskCreationInput{
			ID: "concurrent", PlatformID: downloadtask.PlatformGeneric, Title: "Video",
			SuggestedFilename: "video", SuggestedExtension: ".mp4",
			PlatformDataVersion: 1, PlatformData: platformData,
		})
		if err != nil {
			createErr <- err
			return
		}
		created <- task
	}()
	select {
	case task := <-created:
		t.Fatalf("CreateTask observed uncommitted Settings candidate: %+v", task.OutputPolicy)
	case err := <-createErr:
		t.Fatalf("CreateTask failed before commit boundary released: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(persister.release)
	require.ErrorContains(t, <-updateDone, "commit settings")

	select {
	case task := <-created:
		absoluteOldDir, err := filepath.Abs(oldDir)
		require.NoError(t, err)
		require.Equal(t, absoluteOldDir, task.OutputPolicy.Directory)
	case err := <-createErr:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("CreateTask remained blocked after Settings rollback")
	}
	require.Equal(t, 1, manager.GetMaxConcurrent())
	require.Equal(t, oldDir, configManager.Get().DownloadDir)
}

func TestAppSettingsProxyPortStoppedAppliesWithoutRestartMarker(t *testing.T) {
	current := settings.FromConfig(config.DefaultConfig())
	candidate := current
	candidate.ProxyPort++
	runtime := &fakeSettingsProxyRuntime{port: current.ProxyPort}
	planner := appSettingsEffectPlanner{app: &App{}, proxyRuntime: runtime}

	plan, err := planner.Plan(current, candidate, settings.Diff(current, candidate))
	require.NoError(t, err)
	require.Len(t, plan.CriticalReversible, 1)
	require.Equal(t, "proxy_port", plan.CriticalReversible[0].Name())
	require.Empty(t, plan.DeferredRestart)
	require.NoError(t, plan.CriticalReversible[0].Preflight(context.Background()))
	require.NoError(t, plan.CriticalReversible[0].Apply(context.Background()))
	require.Equal(t, candidate.ProxyPort, runtime.port)
	require.NoError(t, plan.CriticalReversible[0].Rollback(context.Background()))
	require.Equal(t, current.ProxyPort, runtime.port)
}

func TestAppSettingsProxyPortRunningReturnsProxyRestartMarkerWithoutMutation(t *testing.T) {
	current := settings.FromConfig(config.DefaultConfig())
	candidate := current
	candidate.ProxyPort++
	runtime := &fakeSettingsProxyRuntime{running: true, port: current.ProxyPort}
	planner := appSettingsEffectPlanner{app: &App{}, proxyRuntime: runtime}

	plan, err := planner.Plan(current, candidate, settings.Diff(current, candidate))
	require.NoError(t, err)
	require.Empty(t, plan.CriticalReversible)
	require.Len(t, plan.DeferredRestart, 1)
	require.Equal(t, "proxy", plan.DeferredRestart[0].Requirement().Scope)
	require.Equal(t, []string{"proxyPort"}, plan.DeferredRestart[0].Requirement().Fields)
	require.Equal(t, current.ProxyPort, runtime.port)
}

func TestRunningProxyPortChangeAppliesAtNextStartPreparation(t *testing.T) {
	root := t.TempDir()
	manager := config.NewConfigManager(filepath.Join(root, "config.json"))
	require.NoError(t, manager.Load())
	initial := manager.Get()
	initial.DownloadDir = filepath.Join(root, "downloads")
	require.NoError(t, manager.Commit(context.Background(), initial))

	proxyRuntime := &fakeSettingsProxyRuntime{running: true, port: initial.ProxyPort}
	app := &App{configManager: manager}
	app.settingsModule = settings.NewModule(manager, appSettingsEffectPlanner{app: app, proxyRuntime: proxyRuntime})
	newPort := initial.ProxyPort + 1

	result, err := app.settingsModule.UpdateSettings(context.Background(), settings.SettingsPatch{ProxyPort: &newPort})
	require.NoError(t, err)
	require.True(t, result.RestartRequired)
	require.Equal(t, "proxy", result.RestartRequirements[0].Scope)
	require.Equal(t, initial.ProxyPort, proxyRuntime.port, "running listener must not be hot-switched")

	proxyRuntime.running = false // user stops the proxy
	require.NoError(t, prepareProxyRuntimeForStart(proxyRuntime, result.Settings))
	require.Equal(t, newPort, proxyRuntime.port, "next proxy start must use the committed port")
}

func TestAppRuntimeInfoContainsOnlyEffectiveRuntimeMetadata(t *testing.T) {
	manager := config.NewConfigManager(filepath.Join(t.TempDir(), "config.json"))
	require.NoError(t, manager.Load())
	runtimePort := config.DefaultConfig().APIPort + 17
	app := &App{configManager: manager, internalAPI: api.NewInternalAPI(runtimePort)}

	info := app.GetAppInfo()
	require.Equal(t, runtimePort, info.APIPort, "runtime metadata must report the bound port even when settings differ")
	encoded, err := json.Marshal(info)
	require.NoError(t, err)
	payload := string(encoded)
	require.Contains(t, payload, `"version"`)
	require.Contains(t, payload, `"apiPort"`, "the frontend needs the currently bound port, not a pending setting")
	require.NotContains(t, payload, `"proxyPort"`)
	require.NotContains(t, payload, `"downloadDir"`)
	require.NotContains(t, payload, `"theme"`)
}

type fakeSettingsProxyRuntime struct {
	running       bool
	port          int
	upstreamProxy string
	debug         bool
}

type blockingFailurePersister struct {
	mu      sync.Mutex
	fail    bool
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingFailurePersister) armFailure() {
	p.mu.Lock()
	p.fail = true
	p.started = make(chan struct{})
	p.release = make(chan struct{})
	p.once = sync.Once{}
	p.mu.Unlock()
}

func (p *blockingFailurePersister) Persist(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	p.mu.Lock()
	fail, started, release := p.fail, p.started, p.release
	p.mu.Unlock()
	if fail {
		p.once.Do(func() { close(started) })
		select {
		case <-release:
			return errors.New("injected persistence failure")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, perm)
}

func (p *fakeSettingsProxyRuntime) IsRunning() bool { return p.running }
func (p *fakeSettingsProxyRuntime) GetPort() int    { return p.port }
func (p *fakeSettingsProxyRuntime) SetPort(port int) error {
	if p.running && port != p.port {
		return errors.New("proxy is running")
	}
	p.port = port
	return nil
}
func (p *fakeSettingsProxyRuntime) SetUpstreamProxy(value string) { p.upstreamProxy = value }
func (p *fakeSettingsProxyRuntime) SetDebug(value bool)           { p.debug = value }
