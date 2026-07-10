// Package settings owns public user-setting snapshots, patch validation,
// persistence transactions, and explicitly planned runtime side effects.
package settings

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"EasyDownload/internal/config"
)

const (
	defaultCommitTimeout     = 10 * time.Second
	defaultRollbackTimeout   = 5 * time.Second
	defaultBestEffortTimeout = 5 * time.Second
)

// SettingsSnapshot is the complete public read model for user settings. Runtime
// metadata such as the application version, FFmpeg path, and certificate state
// deliberately lives outside this contract.
type SettingsSnapshot struct {
	ProxyPort            int    `json:"proxyPort"`
	APIPort              int    `json:"apiPort"`
	DownloadDir          string `json:"downloadDir"`
	MaxConcurrent        int    `json:"maxConcurrent"`
	MinimizeToTray       bool   `json:"minimizeToTray"`
	ShowNotification     bool   `json:"showNotification"`
	FirstRunComplete     bool   `json:"firstRunComplete"`
	CloseAction          string `json:"closeAction"`
	DontAskOnClose       bool   `json:"dontAskOnClose"`
	Theme                string `json:"theme"`
	Language             string `json:"language"`
	UpstreamProxy        string `json:"upstreamProxy"`
	UseUpstreamProxy     bool   `json:"useUpstreamProxy"`
	ProxyDebug           bool   `json:"proxyDebug"`
	DontRemindCertWizard bool   `json:"dontRemindCertWizard"`
}

// Snapshot is kept as a source-compatible Go alias while callers migrate to
// the explicit contract name. It does not add a second JSON or Wails shape.
type Snapshot = SettingsSnapshot

// SettingsPatch distinguishes omitted fields from their zero values.
type SettingsPatch struct {
	ProxyPort            *int    `json:"proxyPort,omitempty"`
	APIPort              *int    `json:"apiPort,omitempty"`
	DownloadDir          *string `json:"downloadDir,omitempty"`
	MaxConcurrent        *int    `json:"maxConcurrent,omitempty"`
	MinimizeToTray       *bool   `json:"minimizeToTray,omitempty"`
	ShowNotification     *bool   `json:"showNotification,omitempty"`
	FirstRunComplete     *bool   `json:"firstRunComplete,omitempty"`
	CloseAction          *string `json:"closeAction,omitempty"`
	DontAskOnClose       *bool   `json:"dontAskOnClose,omitempty"`
	Theme                *string `json:"theme,omitempty"`
	Language             *string `json:"language,omitempty"`
	UpstreamProxy        *string `json:"upstreamProxy,omitempty"`
	UseUpstreamProxy     *bool   `json:"useUpstreamProxy,omitempty"`
	ProxyDebug           *bool   `json:"proxyDebug,omitempty"`
	DontRemindCertWizard *bool   `json:"dontRemindCertWizard,omitempty"`
}

// Patch is a source-compatible Go alias for SettingsPatch.
type Patch = SettingsPatch

type SettingsWarning struct {
	Code    string `json:"code"`
	Effect  string `json:"effect,omitempty"`
	Message string `json:"message"`
}

type RestartRequirement struct {
	Scope  string   `json:"scope"`
	Fields []string `json:"fields"`
	Reason string   `json:"reason"`
}

type SettingsUpdateResult struct {
	Settings            SettingsSnapshot     `json:"settings"`
	Warnings            []SettingsWarning    `json:"warnings,omitempty"`
	RestartRequired     bool                 `json:"restartRequired"`
	RestartRequirements []RestartRequirement `json:"restartRequirements,omitempty"`
}

type Changed map[string]bool

func (c Changed) Has(name string) bool { return c != nil && c[name] }

// CriticalSettingsEffect must either apply completely or clean up any partial
// mutation before returning an Apply error. Successfully applied effects are
// compensated by the transaction coordinator in reverse order.
type CriticalSettingsEffect interface {
	Name() string
	Preflight(ctx context.Context) error
	Apply(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// CommittedCriticalSettingsEffect is an optional post-durable-commit hook for
// critical effects that must hold an isolation capability across persistence
// (for example a DownloadManager runtime-config write lock). Commit must always
// release that capability before returning, even when ctx is canceled. Errors
// are reported as warnings because the user configuration is already durable.
type CommittedCriticalSettingsEffect interface {
	CriticalSettingsEffect
	Commit(ctx context.Context) error
}

type DeferredSettingsEffect interface {
	Requirement() RestartRequirement
}

type BestEffortSettingsEffect interface {
	Name() string
	Apply(ctx context.Context) error
}

type SettingsEffectPlan struct {
	CriticalReversible []CriticalSettingsEffect
	DeferredRestart    []DeferredSettingsEffect
	BestEffort         []BestEffortSettingsEffect
}

type EffectPlanner interface {
	Plan(current, candidate SettingsSnapshot, changed Changed) (SettingsEffectPlan, error)
}

type EffectPlannerFunc func(current, candidate SettingsSnapshot, changed Changed) (SettingsEffectPlan, error)

func (f EffectPlannerFunc) Plan(current, candidate SettingsSnapshot, changed Changed) (SettingsEffectPlan, error) {
	return f(current, candidate, changed)
}

type NoopEffectPlanner struct{}

func (NoopEffectPlanner) Plan(SettingsSnapshot, SettingsSnapshot, Changed) (SettingsEffectPlan, error) {
	return SettingsEffectPlan{}, nil
}

// ConfigRepository is the only persistence capability needed by Settings.
// ConfigManager implements it with disk-before-memory commit semantics.
type ConfigRepository interface {
	Get() *config.Config
	Update(ctx context.Context, mutate func(candidate *config.Config) error) error
}

// TransactionError retains the primary failure and every compensation failure.
// Callers can use errors.As to surface an explicit inconsistent-state diagnostic.
type TransactionError struct {
	Cause          error
	RollbackErrors []error
}

func (e *TransactionError) Error() string {
	if e == nil {
		return ""
	}
	parts := make([]string, 0, 1+len(e.RollbackErrors))
	if e.Cause != nil {
		parts = append(parts, e.Cause.Error())
	}
	for _, rollbackErr := range e.RollbackErrors {
		parts = append(parts, "rollback: "+rollbackErr.Error())
	}
	return strings.Join(parts, "; ")
}

func (e *TransactionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type Module struct {
	mu                sync.Mutex
	config            ConfigRepository
	planner           EffectPlanner
	commitTimeout     time.Duration
	rollbackTimeout   time.Duration
	bestEffortTimeout time.Duration
}

func NewModule(configRepository ConfigRepository, planner EffectPlanner) *Module {
	if planner == nil {
		planner = NoopEffectPlanner{}
	}
	return &Module{
		config:            configRepository,
		planner:           planner,
		commitTimeout:     defaultCommitTimeout,
		rollbackTimeout:   defaultRollbackTimeout,
		bestEffortTimeout: defaultBestEffortTimeout,
	}
}

func (m *Module) GetSettings(ctx context.Context) (SettingsSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return SettingsSnapshot{}, err
	}
	if m == nil || m.config == nil {
		return FromConfig(config.DefaultConfig()), nil
	}
	return FromConfig(m.config.Get()), nil
}

func (m *Module) UpdateSettings(ctx context.Context, patch SettingsPatch) (SettingsUpdateResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return SettingsUpdateResult{}, err
	}
	if m == nil || m.config == nil {
		return SettingsUpdateResult{}, fmt.Errorf("settings module is not initialized")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return SettingsUpdateResult{}, err
	}

	currentConfig := m.config.Get()
	if currentConfig == nil {
		return SettingsUpdateResult{}, fmt.Errorf("settings repository returned a nil config")
	}
	current := FromConfig(currentConfig)
	result := SettingsUpdateResult{Settings: current}
	candidateConfig := *currentConfig
	ApplyPatch(&candidateConfig, patch)
	candidate := FromConfig(&candidateConfig)
	if err := Validate(candidate); err != nil {
		return result, err
	}

	changed := Diff(current, candidate)
	if len(changed) == 0 {
		return result, nil
	}

	plan, err := m.planner.Plan(current, candidate, cloneChanged(changed))
	if err != nil {
		return result, fmt.Errorf("build settings effect plan: %w", err)
	}
	plan = clonePlan(plan)
	if err := validatePlan(plan); err != nil {
		return result, err
	}
	requirements, err := restartRequirements(plan.DeferredRestart)
	if err != nil {
		return result, err
	}

	for _, effect := range plan.CriticalReversible {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := effect.Preflight(ctx); err != nil {
			return result, fmt.Errorf("preflight settings effect %q: %w", effect.Name(), err)
		}
	}

	applied := make([]CriticalSettingsEffect, 0, len(plan.CriticalReversible))
	for _, effect := range plan.CriticalReversible {
		if err := ctx.Err(); err != nil {
			return result, m.rollback(applied, err)
		}
		if err := effect.Apply(ctx); err != nil {
			return result, m.rollback(applied, fmt.Errorf("apply settings effect %q: %w", effect.Name(), err))
		}
		applied = append(applied, effect)
	}

	// This is the cancellation boundary: before durable commit cancellation is
	// a primary failure and compensates runtime state. Once Commit succeeds the
	// transaction is durable and must be reported as successful.
	if err := ctx.Err(); err != nil {
		return result, m.rollback(applied, err)
	}
	commitCtx, cancelCommit := context.WithTimeout(context.Background(), m.durationOrDefault(m.commitTimeout, defaultCommitTimeout))
	err = m.config.Update(commitCtx, func(latest *config.Config) error {
		if concurrentChanges := Diff(current, FromConfig(latest)); len(concurrentChanges) > 0 {
			return fmt.Errorf("user settings changed outside Settings transaction")
		}
		applySnapshot(latest, candidate)
		return nil
	})
	cancelCommit()
	if err != nil {
		return result, m.rollback(applied, fmt.Errorf("commit settings: %w", err))
	}

	result.Settings = candidate
	result.RestartRequirements = requirements
	result.RestartRequired = len(result.RestartRequirements) > 0
	for i := len(applied) - 1; i >= 0; i-- {
		effect, ok := applied[i].(CommittedCriticalSettingsEffect)
		if !ok {
			continue
		}
		finalizeCtx, cancelFinalize := context.WithTimeout(context.Background(), m.durationOrDefault(m.bestEffortTimeout, defaultBestEffortTimeout))
		finalizeErr := effect.Commit(finalizeCtx)
		cancelFinalize()
		if finalizeErr != nil {
			result.Warnings = append(result.Warnings, SettingsWarning{
				Code:    "settings.commit_finalize_failed",
				Effect:  effect.Name(),
				Message: finalizeErr.Error(),
			})
		}
	}
	for _, effect := range plan.BestEffort {
		effectCtx, cancelEffect := context.WithTimeout(context.Background(), m.durationOrDefault(m.bestEffortTimeout, defaultBestEffortTimeout))
		effectErr := effect.Apply(effectCtx)
		cancelEffect()
		if effectErr != nil {
			result.Warnings = append(result.Warnings, SettingsWarning{
				Code:    "settings.best_effort_failed",
				Effect:  effect.Name(),
				Message: effectErr.Error(),
			})
		}
	}
	if err := ctx.Err(); err != nil {
		result.Warnings = append(result.Warnings, SettingsWarning{
			Code:    "settings.request_canceled_after_commit",
			Message: err.Error(),
		})
	}
	return result, nil
}

func (m *Module) rollback(applied []CriticalSettingsEffect, cause error) error {
	if len(applied) == 0 {
		return cause
	}
	rollbackCtx, cancel := context.WithTimeout(context.Background(), m.durationOrDefault(m.rollbackTimeout, defaultRollbackTimeout))
	defer cancel()

	rollbackErrors := make([]error, 0)
	for i := len(applied) - 1; i >= 0; i-- {
		effect := applied[i]
		if err := effect.Rollback(rollbackCtx); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("%s: %w", effect.Name(), err))
		}
	}
	if len(rollbackErrors) == 0 {
		return cause
	}
	return &TransactionError{Cause: cause, RollbackErrors: rollbackErrors}
}

func (m *Module) durationOrDefault(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func clonePlan(plan SettingsEffectPlan) SettingsEffectPlan {
	return SettingsEffectPlan{
		CriticalReversible: append([]CriticalSettingsEffect(nil), plan.CriticalReversible...),
		DeferredRestart:    append([]DeferredSettingsEffect(nil), plan.DeferredRestart...),
		BestEffort:         append([]BestEffortSettingsEffect(nil), plan.BestEffort...),
	}
}

func validatePlan(plan SettingsEffectPlan) error {
	for i, effect := range plan.CriticalReversible {
		if isNilInterface(effect) {
			return fmt.Errorf("critical settings effect %d is nil", i)
		}
		if strings.TrimSpace(effect.Name()) == "" {
			return fmt.Errorf("critical settings effect %d has no name", i)
		}
	}
	for i, effect := range plan.DeferredRestart {
		if isNilInterface(effect) {
			return fmt.Errorf("deferred settings effect %d is nil", i)
		}
	}
	for i, effect := range plan.BestEffort {
		if isNilInterface(effect) {
			return fmt.Errorf("best-effort settings effect %d is nil", i)
		}
		if strings.TrimSpace(effect.Name()) == "" {
			return fmt.Errorf("best-effort settings effect %d has no name", i)
		}
	}
	return nil
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func restartRequirements(effects []DeferredSettingsEffect) ([]RestartRequirement, error) {
	if len(effects) == 0 {
		return nil, nil
	}
	requirements := make([]RestartRequirement, 0, len(effects))
	for i, effect := range effects {
		requirement := effect.Requirement()
		if requirement.Scope != "app" && requirement.Scope != "proxy" {
			return nil, fmt.Errorf("deferred settings effect %d has invalid restart scope %q", i, requirement.Scope)
		}
		if len(requirement.Fields) == 0 {
			return nil, fmt.Errorf("deferred settings effect %d has no restart fields", i)
		}
		for _, field := range requirement.Fields {
			if strings.TrimSpace(field) == "" {
				return nil, fmt.Errorf("deferred settings effect %d has an empty restart field", i)
			}
		}
		if strings.TrimSpace(requirement.Reason) == "" {
			return nil, fmt.Errorf("deferred settings effect %d has no restart reason", i)
		}
		requirement.Fields = append([]string(nil), requirement.Fields...)
		requirements = append(requirements, requirement)
	}
	return requirements, nil
}

func cloneChanged(changed Changed) Changed {
	result := make(Changed, len(changed))
	for name, value := range changed {
		result[name] = value
	}
	return result
}

func FromConfig(cfg *config.Config) SettingsSnapshot {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	return SettingsSnapshot{
		ProxyPort: cfg.ProxyPort, APIPort: cfg.APIPort,
		DownloadDir: cfg.DownloadDir, MaxConcurrent: cfg.MaxConcurrent,
		MinimizeToTray: cfg.MinimizeToTray, ShowNotification: cfg.ShowNotification,
		FirstRunComplete: cfg.FirstRunComplete, CloseAction: cfg.CloseAction,
		DontAskOnClose: cfg.DontAskOnClose, Theme: cfg.Theme, Language: cfg.Language,
		UpstreamProxy: cfg.UpstreamProxy, UseUpstreamProxy: cfg.UseUpstreamProxy,
		ProxyDebug: cfg.ProxyDebug, DontRemindCertWizard: cfg.DontRemindCertWizard,
	}
}

func ApplyPatch(cfg *config.Config, patch SettingsPatch) {
	if patch.ProxyPort != nil {
		cfg.ProxyPort = *patch.ProxyPort
	}
	if patch.APIPort != nil {
		cfg.APIPort = *patch.APIPort
	}
	if patch.DownloadDir != nil {
		cfg.DownloadDir = strings.TrimSpace(*patch.DownloadDir)
	}
	if patch.MaxConcurrent != nil {
		cfg.MaxConcurrent = *patch.MaxConcurrent
	}
	if patch.MinimizeToTray != nil {
		cfg.MinimizeToTray = *patch.MinimizeToTray
	}
	if patch.ShowNotification != nil {
		cfg.ShowNotification = *patch.ShowNotification
	}
	if patch.FirstRunComplete != nil {
		cfg.FirstRunComplete = *patch.FirstRunComplete
	}
	if patch.CloseAction != nil {
		cfg.CloseAction = strings.TrimSpace(*patch.CloseAction)
	}
	if patch.DontAskOnClose != nil {
		cfg.DontAskOnClose = *patch.DontAskOnClose
	}
	if patch.Theme != nil {
		cfg.Theme = strings.TrimSpace(*patch.Theme)
	}
	if patch.Language != nil {
		cfg.Language = strings.TrimSpace(*patch.Language)
	}
	if patch.UpstreamProxy != nil {
		cfg.UpstreamProxy = strings.TrimSpace(*patch.UpstreamProxy)
	}
	if patch.UseUpstreamProxy != nil {
		cfg.UseUpstreamProxy = *patch.UseUpstreamProxy
	}
	if patch.ProxyDebug != nil {
		cfg.ProxyDebug = *patch.ProxyDebug
	}
	if patch.DontRemindCertWizard != nil {
		cfg.DontRemindCertWizard = *patch.DontRemindCertWizard
	}
}

func applySnapshot(cfg *config.Config, snapshot SettingsSnapshot) {
	cfg.ProxyPort = snapshot.ProxyPort
	cfg.APIPort = snapshot.APIPort
	cfg.DownloadDir = snapshot.DownloadDir
	cfg.MaxConcurrent = snapshot.MaxConcurrent
	cfg.MinimizeToTray = snapshot.MinimizeToTray
	cfg.ShowNotification = snapshot.ShowNotification
	cfg.FirstRunComplete = snapshot.FirstRunComplete
	cfg.CloseAction = snapshot.CloseAction
	cfg.DontAskOnClose = snapshot.DontAskOnClose
	cfg.Theme = snapshot.Theme
	cfg.Language = snapshot.Language
	cfg.UpstreamProxy = snapshot.UpstreamProxy
	cfg.UseUpstreamProxy = snapshot.UseUpstreamProxy
	cfg.ProxyDebug = snapshot.ProxyDebug
	cfg.DontRemindCertWizard = snapshot.DontRemindCertWizard
}

func Validate(snapshot SettingsSnapshot) error {
	if err := validatePort("proxyPort", snapshot.ProxyPort); err != nil {
		return err
	}
	if err := validatePort("apiPort", snapshot.APIPort); err != nil {
		return err
	}
	if snapshot.ProxyPort == snapshot.APIPort {
		return fmt.Errorf("proxyPort and apiPort must be different")
	}
	if snapshot.MaxConcurrent <= 0 || snapshot.MaxConcurrent > 32 {
		return fmt.Errorf("maxConcurrent must be between 1 and 32")
	}
	downloadDir := strings.TrimSpace(snapshot.DownloadDir)
	if downloadDir == "" {
		return fmt.Errorf("downloadDir is required")
	}
	if !filepath.IsAbs(downloadDir) {
		return fmt.Errorf("downloadDir must be absolute")
	}
	if snapshot.Theme != "dark" && snapshot.Theme != "light" {
		return fmt.Errorf("theme must be 'dark' or 'light'")
	}
	if snapshot.Language != "zh-CN" && snapshot.Language != "en-US" {
		return fmt.Errorf("language must be 'zh-CN' or 'en-US'")
	}
	if snapshot.CloseAction != "" && snapshot.CloseAction != "exit" && snapshot.CloseAction != "minimize" {
		return fmt.Errorf("closeAction must be '', 'exit', or 'minimize'")
	}
	upstreamProxy := strings.TrimSpace(snapshot.UpstreamProxy)
	if snapshot.UseUpstreamProxy && upstreamProxy == "" {
		return fmt.Errorf("upstreamProxy is required when useUpstreamProxy is enabled")
	}
	if upstreamProxy != "" {
		u, err := url.Parse(upstreamProxy)
		if err != nil || u.Host == "" {
			return fmt.Errorf("upstreamProxy must be a valid proxy URL")
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "socks5", "socks5h":
		default:
			return fmt.Errorf("upstreamProxy scheme must be http, https, socks5, or socks5h")
		}
	}
	return nil
}

func validatePort(name string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%s must be between 1 and 65535", name)
	}
	return nil
}

func Diff(a, b SettingsSnapshot) Changed {
	changed := Changed{}
	add := func(name string, different bool) {
		if different {
			changed[name] = true
		}
	}
	add("proxyPort", a.ProxyPort != b.ProxyPort)
	add("apiPort", a.APIPort != b.APIPort)
	add("downloadDir", a.DownloadDir != b.DownloadDir)
	add("maxConcurrent", a.MaxConcurrent != b.MaxConcurrent)
	add("minimizeToTray", a.MinimizeToTray != b.MinimizeToTray)
	add("showNotification", a.ShowNotification != b.ShowNotification)
	add("firstRunComplete", a.FirstRunComplete != b.FirstRunComplete)
	add("closeAction", a.CloseAction != b.CloseAction)
	add("dontAskOnClose", a.DontAskOnClose != b.DontAskOnClose)
	add("theme", a.Theme != b.Theme)
	add("language", a.Language != b.Language)
	add("upstreamProxy", a.UpstreamProxy != b.UpstreamProxy)
	add("useUpstreamProxy", a.UseUpstreamProxy != b.UseUpstreamProxy)
	add("proxyDebug", a.ProxyDebug != b.ProxyDebug)
	add("dontRemindCertWizard", a.DontRemindCertWizard != b.DontRemindCertWizard)
	return changed
}
