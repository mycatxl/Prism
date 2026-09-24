package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"prism/internal/intel"
	"prism/internal/intel/checks"
	"prism/internal/intel/providers"
	"prism/internal/intel/store"
	"prism/internal/model"
)

// WP09 §4/§5.4: the settings surface of the intel data sources and the unlock
// checks. Every write is persisted in intel_provider_settings and applied to the
// running registry/engine, so nothing needs a restart; every secret stays in the
// store and the API only reports has_key (R6).

// IntelUpdateCheckRequest is the PATCH /api/v1/intel/checks/{id} body: the only
// accepted field is the enabled toggle. The persisted row is keyed by
// "check:<id>" in intel_provider_settings (WP09 §5.4).
type IntelUpdateCheckRequest struct {
	Enabled *bool `json:"enabled"`
}

// IntelCheckStatus is one entry of GET /api/v1/intel/checks: the rule metadata,
// its source (builtin or user), the file a user rule was loaded from and the
// effective enabled state.
type IntelCheckStatus struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Version    int      `json:"version"`
	Category   string   `json:"category"`
	Source     string   `json:"source"`
	Path       string   `json:"path,omitempty"`
	Calibrated string   `json:"calibrated,omitempty"`
	TTL        string   `json:"ttl"`
	Timeout    string   `json:"timeout"`
	Steps      []string `json:"steps"`
	// Enabled is the effective state: the persisted toggle when there is one,
	// otherwise the rule's own default.
	Enabled bool `json:"enabled"`
	// EnabledOverride marks a rule whose state comes from the settings page.
	EnabledOverride bool `json:"enabled_override"`
}

// IntelCheckLoadError is a user rule file that could not be loaded, so a user
// can see why their rule did not appear.
type IntelCheckLoadError struct {
	Path  string `json:"path,omitempty"`
	Error string `json:"error"`
}

// IntelChecks is the GET /api/v1/intel/checks payload.
type IntelChecks struct {
	Items      []IntelCheckStatus    `json:"items"`
	LoadErrors []IntelCheckLoadError `json:"load_errors"`
}

// IntelProviderList returns every registered data source with its effective,
// non-secret settings and its live budget/queue state (WP09 §4).
func (s *ControlPlaneService) IntelProviderList(ctx context.Context) ([]providers.ProviderStatus, error) {
	svc, settings, serr := s.intelProviderSettings()
	if serr != nil {
		return nil, serr
	}
	usage, err := intelProviderUsage(ctx, svc.Store())
	if err != nil {
		return nil, internal("read provider state", err)
	}
	statuses, err := settings.Statuses(usage)
	if err != nil {
		return nil, mapIntelSettingsError(err)
	}
	return statuses, nil
}

// IntelUpdateProvider validates and persists one settings patch and returns the
// provider together with its live state. The registry is updated in place, so
// the running pipeline and the queue workers use the new settings immediately.
func (s *ControlPlaneService) IntelUpdateProvider(ctx context.Context, id string, patch providers.ProviderPatch) (providers.ProviderStatus, error) {
	svc, settings, serr := s.intelProviderSettings()
	if serr != nil {
		return providers.ProviderStatus{}, serr
	}
	usage, err := intelProviderUsage(ctx, svc.Store())
	if err != nil {
		return providers.ProviderStatus{}, internal("read provider state", err)
	}
	status, err := settings.Update(id, patch, usage)
	if err != nil {
		return providers.ProviderStatus{}, mapIntelSettingsError(err)
	}
	return status, nil
}

// IntelResumeProvider clears the paused flag and the 429 cooldown of one
// provider (WP09 §4).
func (s *ControlPlaneService) IntelResumeProvider(ctx context.Context, id string) (providers.ProviderStatus, error) {
	svc, settings, serr := s.intelProviderSettings()
	if serr != nil {
		return providers.ProviderStatus{}, serr
	}
	id = strings.TrimSpace(id)
	if _, ok := settings.Registry().Spec(id); !ok {
		return providers.ProviderStatus{}, notFound("provider not found")
	}
	if err := svc.Store().ResumeProvider(ctx, id); err != nil {
		return providers.ProviderStatus{}, internal("resume provider", err)
	}
	usage, err := intelProviderUsage(ctx, svc.Store())
	if err != nil {
		return providers.ProviderStatus{}, internal("read provider state", err)
	}
	return settings.Status(id, usage)
}

// IntelRefreshProvider downloads the offline databases of one data source now
// (WP09 §3). The call is bounded by the refresher's manual timeout and reports
// what happened file by file: a data source that is not configured is a
// recorded skip, a download failure is a recorded failure, and neither is an
// error of the request. Only an unknown provider fails here.
func (s *ControlPlaneService) IntelRefreshProvider(ctx context.Context, id string) (providers.GeoRefreshResult, error) {
	_, settings, serr := s.intelProviderSettings()
	if serr != nil {
		return providers.GeoRefreshResult{}, serr
	}
	result, err := settings.RefreshDatabases(ctx, id)
	if err != nil {
		return providers.GeoRefreshResult{}, mapIntelSettingsError(err)
	}
	return result, nil
}

// IntelChecks returns every unlock check rule plus the user rule files that
// failed to load (WP09 §5.4).
func (s *ControlPlaneService) IntelChecks() (IntelChecks, error) {
	svc, serr := s.intelService()
	if serr != nil {
		return IntelChecks{}, serr
	}
	engine := svc.CheckEngine()
	if engine == nil {
		return IntelChecks{}, conflict("intel checks are not available")
	}
	overrides := s.intelCheckOverrides()
	infos := engine.Rules()
	items := make([]IntelCheckStatus, 0, len(infos))
	for _, info := range infos {
		_, overridden := overrides[info.ID]
		items = append(items, IntelCheckStatus{
			ID: info.ID, Name: info.Name, Version: info.Version,
			Category: info.Category, Source: info.Source, Path: info.Path,
			Calibrated: info.Calibrated, TTL: info.TTL, Timeout: info.Timeout,
			Steps: info.Steps, Enabled: info.Enabled, EnabledOverride: overridden,
		})
	}
	loadErrors := make([]IntelCheckLoadError, 0)
	for _, err := range engine.LoadErrors() {
		if err == nil {
			continue
		}
		loadErrors = append(loadErrors, IntelCheckLoadError{Path: ruleErrorPath(err), Error: err.Error()})
	}
	return IntelChecks{Items: items, LoadErrors: loadErrors}, nil
}

// IntelUpdateCheck persists the enabled toggle of one rule under the
// "check:<id>" provider id. The engine resolves that override on every run, so
// the toggle is effective without a restart.
func (s *ControlPlaneService) IntelUpdateCheck(id string, req IntelUpdateCheckRequest) (IntelCheckStatus, error) {
	svc, serr := s.intelService()
	if serr != nil {
		return IntelCheckStatus{}, serr
	}
	engine := svc.CheckEngine()
	if engine == nil {
		return IntelCheckStatus{}, conflict("intel checks are not available")
	}
	id = strings.TrimSpace(id)
	if req.Enabled == nil {
		return IntelCheckStatus{}, invalidArg("enabled: is required")
	}
	if _, ok := engine.Rule(id); !ok {
		return IntelCheckStatus{}, notFound("check not found")
	}
	if s.Engine == nil {
		return IntelCheckStatus{}, conflict("state storage is not available")
	}
	row := model.IntelProviderSetting{
		ProviderID:  checks.CheckSettingID(id),
		Enabled:     *req.Enabled,
		UpdatedAtNs: time.Now().UTC().UnixNano(),
	}
	if err := s.Engine.UpsertIntelProviderSetting(row); err != nil {
		return IntelCheckStatus{}, internal("persist check toggle", err)
	}
	// Report the effective state the engine applies on the next run.
	checksList, err := s.IntelChecks()
	if err != nil {
		return IntelCheckStatus{}, err
	}
	for _, item := range checksList.Items {
		if item.ID == id {
			if !item.EnabledOverride {
				// The engine's enabled source is not backed by this store; the
				// persisted value is still what was asked for.
				item.EnabledOverride = true
			}
			return item, nil
		}
	}
	return IntelCheckStatus{ID: id, Enabled: *req.Enabled, EnabledOverride: true}, nil
}

// intelProviderSettings resolves the data source settings surface.
func (s *ControlPlaneService) intelProviderSettings() (*intel.Service, *providers.SettingsService, error) {
	svc, serr := s.intelService()
	if serr != nil {
		return nil, nil, serr
	}
	settings := svc.ProviderSettings()
	if settings == nil {
		return nil, nil, conflict("intel provider settings are not available")
	}
	return svc, settings, nil
}

// intelCheckOverrides reports the rules with a persisted enabled toggle.
func (s *ControlPlaneService) intelCheckOverrides() map[string]model.IntelProviderSetting {
	overrides := map[string]model.IntelProviderSetting{}
	if s == nil || s.Engine == nil {
		return overrides
	}
	rows, err := s.Engine.ListIntelProviderSettings()
	if err != nil {
		return overrides
	}
	for _, row := range rows {
		if id, ok := strings.CutPrefix(row.ProviderID, "check:"); ok && id != "" {
			overrides[id] = row
		}
	}
	return overrides
}

// ruleErrorPath extracts the rule file of a load error; the loader renders
// "rule <path>: <reason>". A different shape leaves the path empty.
func ruleErrorPath(err error) string {
	if err == nil {
		return ""
	}
	rest, ok := strings.CutPrefix(err.Error(), "rule ")
	if !ok {
		return ""
	}
	path, _, ok := strings.Cut(rest, ": ")
	if !ok {
		return ""
	}
	return path
}

// intelProviderUsage reads provider_state and provider_queue once and serves the
// per-provider lookup from memory: one list request stays a bounded number of
// queries instead of one per provider.
func intelProviderUsage(ctx context.Context, st *store.Store) (providers.UsageFunc, error) {
	if st == nil {
		return nil, nil
	}
	states, err := st.ListProviderStates(ctx)
	if err != nil {
		return nil, err
	}
	usage := make(map[string]providers.ProviderUsage, len(states))
	for _, state := range states {
		counts, err := st.ProviderQueueCounts(ctx, state.Provider)
		if err != nil {
			return nil, err
		}
		usage[state.Provider] = providers.ProviderUsage{
			Day:             state.Day,
			Used:            state.Used,
			NextRequestAtNs: state.NextRequestAtNs,
			BlockedUntilNs:  state.BlockedUntilNs,
			Paused:          state.Paused,
			ErrorCode:       state.ErrorCode,
			Queued:          counts.Queued,
			Running:         counts.Running,
			Done:            counts.Done,
			Failed:          counts.Failed,
		}
	}
	today := store.DayString(time.Now())
	return func(providerID string) providers.ProviderUsage {
		if entry, ok := usage[providerID]; ok {
			return entry
		}
		return providers.ProviderUsage{Day: today}
	}, nil
}

// mapIntelSettingsError maps the provider settings sentinels onto the documented
// status codes (WP09 §4).
func mapIntelSettingsError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, providers.ErrUnknownProvider):
		return notFound("provider not found")
	case errors.Is(err, providers.ErrInvalidSetting):
		return invalidArg(err.Error())
	case errors.Is(err, providers.ErrSettingsUnavailable):
		return conflict("intel provider settings are not available")
	case errors.Is(err, providers.ErrGeoUnknownProvider):
		return conflict("this data source has no downloadable database")
	case errors.Is(err, providers.ErrGeoUnavailable):
		return conflict("offline database downloads are not available")
	default:
		return internal("intel provider settings", err)
	}
}
