package settings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"EasyDownload/internal/config"

	"github.com/stretchr/testify/require"
)

func TestUpdateSettingsAppliesPatchAndPersistsTypedResult(t *testing.T) {
	cm := newTestConfigManager(t)
	module := NewModule(cm, nil)

	theme := "light"
	show := false
	got, err := module.UpdateSettings(context.Background(), SettingsPatch{
		Theme:            &theme,
		ShowNotification: &show,
	})
	require.NoError(t, err)
	require.Equal(t, "light", got.Settings.Theme)
	require.False(t, got.Settings.ShowNotification)
	require.False(t, got.RestartRequired)
	require.Empty(t, got.Warnings)
	require.Equal(t, "light", cm.Get().Theme)
	require.False(t, cm.Get().ShowNotification)
}

func TestUpdateSettingsRejectsInvalidPatchWithoutPersisting(t *testing.T) {
	cm := newTestConfigManager(t)
	repository := &recordingRepository{base: cm}
	module := NewModule(repository, nil)

	theme := "blue"
	before := cm.Get()
	result, err := module.UpdateSettings(context.Background(), SettingsPatch{Theme: &theme})
	require.Error(t, err)
	require.Equal(t, before.Theme, result.Settings.Theme)
	require.Equal(t, before.Theme, cm.Get().Theme)
	require.Zero(t, repository.commitCount())
}

func TestUpdateSettingsValidatesUpstreamProxyCombination(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		proxy   string
		wantErr string
	}{
		{name: "enabled requires endpoint", enabled: true, wantErr: "upstreamProxy is required"},
		{name: "disabled still rejects unsupported scheme", proxy: "ftp://127.0.0.1:21", wantErr: "scheme must be http, https, socks5, or socks5h"},
		{name: "enabled rejects unsupported scheme", enabled: true, proxy: "ftp://127.0.0.1:21", wantErr: "scheme must be http, https, socks5, or socks5h"},
		{name: "enabled accepts http endpoint", enabled: true, proxy: "http://127.0.0.1:7890"},
		{name: "enabled accepts https endpoint", enabled: true, proxy: "https://proxy.example:8443"},
		{name: "enabled accepts socks5 endpoint", enabled: true, proxy: "socks5://127.0.0.1:1080"},
		{name: "enabled accepts socks5h endpoint", enabled: true, proxy: "socks5h://proxy.example:1080"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cm := newTestConfigManager(t)
			repository := &recordingRepository{base: cm}
			module := NewModule(repository, nil)
			result, err := module.UpdateSettings(context.Background(), SettingsPatch{
				UseUpstreamProxy: &test.enabled,
				UpstreamProxy:    &test.proxy,
			})
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				require.Zero(t, repository.commitCount())
				return
			}
			require.NoError(t, err)
			require.True(t, result.Settings.UseUpstreamProxy)
			require.Equal(t, test.proxy, result.Settings.UpstreamProxy)
			require.Equal(t, 1, repository.commitCount())
		})
	}
}

func TestPureValidationFailureDoesNotCreateDownloadDirectory(t *testing.T) {
	root := t.TempDir()
	candidate := FromConfig(config.DefaultConfig())
	candidate.DownloadDir = filepath.Join(root, "must-not-be-created")
	candidate.Theme = "invalid"

	err := Validate(candidate)
	require.ErrorContains(t, err, "theme")
	_, statErr := os.Stat(candidate.DownloadDir)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestPureValidationDoesNotCreateValidMissingDownloadDirectory(t *testing.T) {
	root := t.TempDir()
	candidate := FromConfig(config.DefaultConfig())
	candidate.DownloadDir = filepath.Join(root, "offline", "downloads")

	require.NoError(t, Validate(candidate))
	require.NoDirExists(t, candidate.DownloadDir)
}

func TestPureSettingsUpdateDoesNotProbeUnchangedDownloadDirectory(t *testing.T) {
	cm := newTestConfigManager(t)
	missingDir := filepath.Join(t.TempDir(), "offline", "downloads")
	candidate := cm.Get()
	candidate.DownloadDir = missingDir
	require.NoError(t, cm.Commit(context.Background(), candidate))
	require.NoDirExists(t, missingDir)

	module := NewModule(cm, nil)
	theme := "light"
	result, err := module.UpdateSettings(context.Background(), SettingsPatch{Theme: &theme})

	require.NoError(t, err)
	require.Equal(t, "light", result.Settings.Theme)
	require.NoDirExists(t, missingDir)
}

func TestUpdateSettingsNoopSkipsPlanAndCommit(t *testing.T) {
	cm := newTestConfigManager(t)
	repository := &recordingRepository{base: cm}
	planCalls := 0
	module := NewModule(repository, EffectPlannerFunc(func(SettingsSnapshot, SettingsSnapshot, Changed) (SettingsEffectPlan, error) {
		planCalls++
		return SettingsEffectPlan{}, nil
	}))

	theme := cm.Get().Theme
	result, err := module.UpdateSettings(context.Background(), SettingsPatch{Theme: &theme})
	require.NoError(t, err)
	require.Equal(t, theme, result.Settings.Theme)
	require.Zero(t, planCalls)
	require.Zero(t, repository.commitCount())
}

func TestPreflightFailureDoesNotApplyOrCommit(t *testing.T) {
	cm := newTestConfigManager(t)
	repository := &recordingRepository{base: cm}
	calls := []string{}
	effect := &recordingCriticalEffect{name: "download-dir", calls: &calls, preflightErr: errors.New("cannot apply")}
	module := NewModule(repository, staticPlan(SettingsEffectPlan{CriticalReversible: []CriticalSettingsEffect{effect}}))

	theme := "light"
	_, err := module.UpdateSettings(context.Background(), SettingsPatch{Theme: &theme})
	require.ErrorContains(t, err, "preflight settings effect")
	require.Equal(t, []string{"preflight:download-dir"}, calls)
	require.Zero(t, repository.commitCount())
	require.Equal(t, "dark", cm.Get().Theme)
}

func TestNthApplyFailureRollsBackOnlyAppliedEffectsInReverseOrder(t *testing.T) {
	cm := newTestConfigManager(t)
	calls := []string{}
	first := &recordingCriticalEffect{name: "first", calls: &calls}
	second := &recordingCriticalEffect{name: "second", calls: &calls, applyErr: errors.New("second failed")}
	module := NewModule(cm, staticPlan(SettingsEffectPlan{
		CriticalReversible: []CriticalSettingsEffect{first, second},
	}))

	theme := "light"
	_, err := module.UpdateSettings(context.Background(), SettingsPatch{Theme: &theme})
	require.ErrorContains(t, err, "second failed")
	require.Equal(t, []string{
		"preflight:first", "preflight:second",
		"apply:first", "apply:second", "rollback:first",
	}, calls)
	require.Equal(t, "dark", cm.Get().Theme)
}

func TestCommitFailureRollsBackAllEffectsAndPreservesConfig(t *testing.T) {
	for _, stage := range []string{"temporary write", "sync", "atomic replace"} {
		t.Run(stage, func(t *testing.T) {
			cm := newTestConfigManager(t)
			repository := &recordingRepository{base: cm, commitErr: errors.New("injected " + stage + " failure")}
			calls := []string{}
			first := &recordingCriticalEffect{name: "first", calls: &calls}
			second := &recordingCriticalEffect{name: "second", calls: &calls}
			module := NewModule(repository, staticPlan(SettingsEffectPlan{
				CriticalReversible: []CriticalSettingsEffect{first, second},
			}))

			theme := "light"
			_, err := module.UpdateSettings(context.Background(), SettingsPatch{Theme: &theme})
			require.ErrorContains(t, err, "commit settings")
			require.Equal(t, []string{
				"preflight:first", "preflight:second",
				"apply:first", "apply:second", "rollback:second", "rollback:first",
			}, calls)
			require.Equal(t, "dark", cm.Get().Theme)
		})
	}
}

func TestRollbackFailuresAreRetainedWithPrimaryError(t *testing.T) {
	cm := newTestConfigManager(t)
	calls := []string{}
	first := &recordingCriticalEffect{name: "first", calls: &calls, rollbackErr: errors.New("rollback failed")}
	second := &recordingCriticalEffect{name: "second", calls: &calls, applyErr: errors.New("apply failed")}
	module := NewModule(cm, staticPlan(SettingsEffectPlan{
		CriticalReversible: []CriticalSettingsEffect{first, second},
	}))

	theme := "light"
	_, err := module.UpdateSettings(context.Background(), SettingsPatch{Theme: &theme})
	var transactionErr *TransactionError
	require.ErrorAs(t, err, &transactionErr)
	require.ErrorContains(t, transactionErr.Cause, "apply failed")
	require.Len(t, transactionErr.RollbackErrors, 1)
	require.ErrorContains(t, transactionErr.RollbackErrors[0], "rollback failed")
}

func TestBestEffortFailureReturnsWarningAfterSuccessfulCommit(t *testing.T) {
	cm := newTestConfigManager(t)
	bestEffort := &recordingBestEffortEffect{name: "publish", err: errors.New("event bus unavailable")}
	deferred := fixedDeferredEffect{requirement: RestartRequirement{
		Scope: "app", Fields: []string{"apiPort"}, Reason: "restart API listener",
	}}
	module := NewModule(cm, staticPlan(SettingsEffectPlan{
		DeferredRestart: []DeferredSettingsEffect{deferred},
		BestEffort:      []BestEffortSettingsEffect{bestEffort},
	}))

	language := "en-US"
	result, err := module.UpdateSettings(context.Background(), SettingsPatch{Language: &language})
	require.NoError(t, err)
	require.Equal(t, "en-US", cm.Get().Language)
	require.True(t, result.RestartRequired)
	require.Equal(t, []string{"apiPort"}, result.RestartRequirements[0].Fields)
	require.Equal(t, "settings.best_effort_failed", result.Warnings[0].Code)
	require.Equal(t, "publish", result.Warnings[0].Effect)
}

func TestCommittedCriticalEffectFinalizesOnlyAfterDurableCommit(t *testing.T) {
	cm := newTestConfigManager(t)
	calls := []string{}
	effect := &recordingCommittedCriticalEffect{name: "runtime-config", calls: &calls}
	module := NewModule(cm, staticPlan(SettingsEffectPlan{
		CriticalReversible: []CriticalSettingsEffect{effect},
	}))
	theme := "light"
	result, err := module.UpdateSettings(context.Background(), SettingsPatch{Theme: &theme})
	require.NoError(t, err)
	require.Empty(t, result.Warnings)
	require.Equal(t, []string{"preflight:runtime-config", "apply:runtime-config", "commit:runtime-config"}, calls)
	require.Equal(t, "light", cm.Get().Theme)
}

func TestCommittedCriticalEffectRollsBackWithoutFinalizeOnPersistenceFailure(t *testing.T) {
	cm := newTestConfigManager(t)
	repository := &recordingRepository{base: cm, commitErr: errors.New("disk unavailable")}
	calls := []string{}
	effect := &recordingCommittedCriticalEffect{name: "runtime-config", calls: &calls}
	module := NewModule(repository, staticPlan(SettingsEffectPlan{
		CriticalReversible: []CriticalSettingsEffect{effect},
	}))
	theme := "light"
	_, err := module.UpdateSettings(context.Background(), SettingsPatch{Theme: &theme})
	require.ErrorContains(t, err, "commit settings")
	require.Equal(t, []string{"preflight:runtime-config", "apply:runtime-config", "rollback:runtime-config"}, calls)
}

func TestInvalidRestartRequirementFailsBeforeCommit(t *testing.T) {
	cm := newTestConfigManager(t)
	repository := &recordingRepository{base: cm}
	module := NewModule(repository, staticPlan(SettingsEffectPlan{
		DeferredRestart: []DeferredSettingsEffect{fixedDeferredEffect{requirement: RestartRequirement{
			Scope: "unknown", Fields: []string{"apiPort"}, Reason: "restart",
		}}},
	}))

	language := "en-US"
	_, err := module.UpdateSettings(context.Background(), SettingsPatch{Language: &language})
	require.ErrorContains(t, err, "invalid restart scope")
	require.Zero(t, repository.commitCount())
	require.Equal(t, "zh-CN", cm.Get().Language)
}

func TestCancellationBeforeCommitRollsBackWithIndependentContext(t *testing.T) {
	cm := newTestConfigManager(t)
	repository := &recordingRepository{base: cm}
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	calls := []string{}
	effect := &recordingCriticalEffect{name: "runtime", calls: &calls, onApply: cancelRequest}
	module := NewModule(repository, staticPlan(SettingsEffectPlan{
		CriticalReversible: []CriticalSettingsEffect{effect},
	}))

	theme := "light"
	_, err := module.UpdateSettings(requestCtx, SettingsPatch{Theme: &theme})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, []string{"preflight:runtime", "apply:runtime", "rollback:runtime"}, calls)
	require.Zero(t, repository.commitCount())
	require.Equal(t, "dark", cm.Get().Theme)
}

func TestCancellationAfterDurableCommitReturnsSuccessWarning(t *testing.T) {
	cm := newTestConfigManager(t)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	repository := &recordingRepository{base: cm, afterCommit: cancelRequest}
	module := NewModule(repository, nil)

	theme := "light"
	result, err := module.UpdateSettings(requestCtx, SettingsPatch{Theme: &theme})
	require.NoError(t, err)
	require.Equal(t, "light", cm.Get().Theme)
	require.Equal(t, "settings.request_canceled_after_commit", result.Warnings[0].Code)
}

func TestSettingsCommitPreservesConcurrentRuntimeMetadata(t *testing.T) {
	cm := newTestConfigManager(t)
	repository := &recordingRepository{base: cm}
	repository.beforeCommit = func() {
		path := "runtime-discovered-ffmpeg"
		require.NoError(t, cm.UpdateRuntimeMetadata(context.Background(), config.RuntimeMetadataPatch{FFmpegPath: &path}))
	}
	module := NewModule(repository, nil)

	theme := "light"
	result, err := module.UpdateSettings(context.Background(), SettingsPatch{Theme: &theme})
	require.NoError(t, err)
	require.Equal(t, "light", result.Settings.Theme)
	require.Equal(t, "runtime-discovered-ffmpeg", cm.Get().FFmpegPath)
}

func TestSettingsCommitRejectsConcurrentUserSettingWriter(t *testing.T) {
	cm := newTestConfigManager(t)
	repository := &recordingRepository{base: cm}
	repository.beforeCommit = func() {
		require.NoError(t, cm.Update(context.Background(), func(candidate *config.Config) error {
			candidate.Language = "en-US"
			return nil
		}))
	}
	calls := []string{}
	effect := &recordingCriticalEffect{name: "runtime", calls: &calls}
	module := NewModule(repository, staticPlan(SettingsEffectPlan{
		CriticalReversible: []CriticalSettingsEffect{effect},
	}))

	theme := "light"
	_, err := module.UpdateSettings(context.Background(), SettingsPatch{Theme: &theme})
	require.ErrorContains(t, err, "user settings changed outside Settings transaction")
	require.Equal(t, []string{"preflight:runtime", "apply:runtime", "rollback:runtime"}, calls)
	require.Equal(t, "dark", cm.Get().Theme)
	require.Equal(t, "en-US", cm.Get().Language)
}

func TestConcurrentUpdatesAreSerializedAgainstLatestCommittedSnapshot(t *testing.T) {
	cm := newTestConfigManager(t)
	firstApplyStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var observedMu sync.Mutex
	observedLanguageCurrentTheme := ""
	planner := EffectPlannerFunc(func(current, _ SettingsSnapshot, changed Changed) (SettingsEffectPlan, error) {
		if changed.Has("theme") {
			return SettingsEffectPlan{CriticalReversible: []CriticalSettingsEffect{
				&blockingCriticalEffect{started: firstApplyStarted, release: releaseFirst},
			}}, nil
		}
		if changed.Has("language") {
			observedMu.Lock()
			observedLanguageCurrentTheme = current.Theme
			observedMu.Unlock()
		}
		return SettingsEffectPlan{}, nil
	})
	module := NewModule(cm, planner)

	firstDone := make(chan error, 1)
	go func() {
		theme := "light"
		_, err := module.UpdateSettings(context.Background(), SettingsPatch{Theme: &theme})
		firstDone <- err
	}()
	<-firstApplyStarted

	secondDone := make(chan error, 1)
	go func() {
		language := "en-US"
		_, err := module.UpdateSettings(context.Background(), SettingsPatch{Language: &language})
		secondDone <- err
	}()
	close(releaseFirst)

	require.NoError(t, <-firstDone)
	require.NoError(t, <-secondDone)
	observedMu.Lock()
	require.Equal(t, "light", observedLanguageCurrentTheme)
	observedMu.Unlock()
	require.Equal(t, "light", cm.Get().Theme)
	require.Equal(t, "en-US", cm.Get().Language)
}

func TestPurePersistedFieldNeedsNoEffectOrWailsMethod(t *testing.T) {
	cm := newTestConfigManager(t)
	module := NewModule(cm, nil)
	complete := true

	result, err := module.UpdateSettings(context.Background(), SettingsPatch{FirstRunComplete: &complete})
	require.NoError(t, err)
	require.True(t, result.Settings.FirstRunComplete)
	require.Empty(t, result.Warnings)
	require.Empty(t, result.RestartRequirements)
}

func newTestConfigManager(t *testing.T) *config.ConfigManager {
	t.Helper()
	root := t.TempDir()
	cm := config.NewConfigManager(filepath.Join(root, "config.json"))
	require.NoError(t, cm.Load())
	next := cm.Get()
	next.DownloadDir = filepath.Join(root, "downloads")
	require.NoError(t, cm.Commit(context.Background(), next))
	return cm
}

type recordingRepository struct {
	base         *config.ConfigManager
	commitErr    error
	beforeCommit func()
	afterCommit  func()
	mu           sync.Mutex
	commits      int
}

func (r *recordingRepository) Get() *config.Config { return r.base.Get() }

func (r *recordingRepository) Update(ctx context.Context, mutate func(*config.Config) error) error {
	r.mu.Lock()
	r.commits++
	r.mu.Unlock()
	if r.commitErr != nil {
		return r.commitErr
	}
	if r.beforeCommit != nil {
		r.beforeCommit()
	}
	err := r.base.Update(ctx, mutate)
	if err == nil && r.afterCommit != nil {
		r.afterCommit()
	}
	return err
}

func (r *recordingRepository) commitCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.commits
}

func staticPlan(plan SettingsEffectPlan) EffectPlanner {
	return EffectPlannerFunc(func(SettingsSnapshot, SettingsSnapshot, Changed) (SettingsEffectPlan, error) {
		return plan, nil
	})
}

type recordingCriticalEffect struct {
	name         string
	calls        *[]string
	preflightErr error
	applyErr     error
	rollbackErr  error
	onApply      func()
}

func (e *recordingCriticalEffect) Name() string { return e.name }
func (e *recordingCriticalEffect) Preflight(context.Context) error {
	*e.calls = append(*e.calls, "preflight:"+e.name)
	return e.preflightErr
}
func (e *recordingCriticalEffect) Apply(context.Context) error {
	*e.calls = append(*e.calls, "apply:"+e.name)
	if e.onApply != nil {
		e.onApply()
	}
	return e.applyErr
}
func (e *recordingCriticalEffect) Rollback(ctx context.Context) error {
	*e.calls = append(*e.calls, "rollback:"+e.name)
	if err := ctx.Err(); err != nil {
		return err
	}
	return e.rollbackErr
}

type recordingBestEffortEffect struct {
	name string
	err  error
}

type recordingCommittedCriticalEffect struct {
	name  string
	calls *[]string
}

func (e *recordingCommittedCriticalEffect) Name() string { return e.name }
func (e *recordingCommittedCriticalEffect) Preflight(context.Context) error {
	*e.calls = append(*e.calls, "preflight:"+e.name)
	return nil
}
func (e *recordingCommittedCriticalEffect) Apply(context.Context) error {
	*e.calls = append(*e.calls, "apply:"+e.name)
	return nil
}
func (e *recordingCommittedCriticalEffect) Rollback(context.Context) error {
	*e.calls = append(*e.calls, "rollback:"+e.name)
	return nil
}
func (e *recordingCommittedCriticalEffect) Commit(context.Context) error {
	*e.calls = append(*e.calls, "commit:"+e.name)
	return nil
}

func (e *recordingBestEffortEffect) Name() string { return e.name }
func (e *recordingBestEffortEffect) Apply(context.Context) error {
	return e.err
}

type fixedDeferredEffect struct{ requirement RestartRequirement }

func (e fixedDeferredEffect) Requirement() RestartRequirement { return e.requirement }

type blockingCriticalEffect struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (*blockingCriticalEffect) Name() string                    { return "blocking" }
func (*blockingCriticalEffect) Preflight(context.Context) error { return nil }
func (*blockingCriticalEffect) Rollback(context.Context) error  { return nil }
func (e *blockingCriticalEffect) Apply(context.Context) error {
	close(e.started)
	<-e.release
	return nil
}
