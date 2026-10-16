package service

import (
	"errors"
	"strings"
	"time"

	"prism/internal/config"
	"prism/internal/model"
	"prism/internal/state"
)

// Usage alerts for the console.
//
// An alert is a record that one account moved more than its share of a plan
// quota inside a short window, written by the scanner below and read by an
// operator. The point is early warning: a reseller account that burns a monthly
// quota in an hour is either a mistake or abuse, and both are worth knowing
// before the quota is gone.
//
// The row carries the numbers it was decided on - the window, the ratio, what
// was used and the quota it was compared to - so the log still explains itself
// after the plan it refers to was edited or deleted.

// AlertView is one alert as the console renders it: the stored row plus the
// names it does not carry.
type AlertView struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// UserID is the account the traffic belonged to and Username is its login
	// name, resolved when the alert is read. The id is what the console links
	// to; the name is what an operator recognizes.
	UserID         string  `json:"user_id"`
	Username       string  `json:"username"`
	PlanID         string  `json:"plan_id"`
	PlanName       string  `json:"plan_name"`
	WindowMinutes  int     `json:"window_minutes"`
	ThresholdRatio float64 `json:"threshold_ratio"`
	UsedBytes      int64   `json:"used_bytes"`
	QuotaBytes     int64   `json:"quota_bytes"`
	DetectedAtNs   int64   `json:"detected_at_ns"`
	// AcknowledgedAtNs of 0 means the alert is still open.
	AcknowledgedAtNs int64  `json:"acknowledged_at_ns"`
	Note             string `json:"note"`
}

// Alert list bounds. Fifty is a screenful, and 500 is what a console paging
// through the log may ask for; anything beyond that is clamped rather than
// served, because an alert log grows with the deployment and an unbounded read
// would let one request pull all of it.
const (
	defaultAlertLimit = 50
	maxAlertLimit     = 500
)

// alertSubject is the pair the scan refuses to record twice inside one window:
// one subscription's own kind of anomaly. Keyed by subscription rather than by
// user because a user can have an old and a new subscription at the same time,
// and the open alert belongs to the one that generated the traffic.
type alertSubject struct {
	subscriptionID string
	kind           string
}

// alertScanSettings is the resolved form of the three alert settings.
//
// The ratio and the window in minutes are what gets recorded on the row; the two
// nanosecond values are what the reads use. All four are resolved in one place so
// the log and the query can never disagree about the window they describe.
type alertScanSettings struct {
	ratio         float64
	windowMinutes int
	windowNs      int64
	retentionNs   int64
}

// ScanUsageAlerts records one alert for every active subscription whose traffic
// in the last alert window reached the configured share of its plan quota, and
// returns how many alerts it created.
//
// The measurement is the accounting the usage pages read: both directions summed
// over the window for the subscription's user. A plan with no traffic limit
// (quota 0) never alerts, because "unlimited" is not a budget that can be run out
// of.
//
// An account that already has an open alert of the same kind inside the same
// window is left alone: this runs on a timer, and one runaway account must not
// fill the log with a row per tick. Acknowledging the alert is what lets the
// next crossing be reported again.
//
// Alerts older than the retention window are pruned in the same call, so the
// table is bounded by the very call that fills it.
func (s *ControlPlaneService) ScanUsageAlerts(nowNs int64) (int, error) {
	if s == nil || s.Engine == nil {
		return 0, internal("no state engine", nil)
	}
	// A detection time of zero is the value the readers treat as "never
	// acknowledged, never aged out"; refusing it here is cheaper than
	// explaining rows that sort to the far end of the log.
	if nowNs <= 0 {
		return 0, invalidArg("now_ns: must be positive")
	}

	settings := s.alertSettings()
	if _, err := s.Engine.DeleteAlertsBefore(nowNs - settings.retentionNs); err != nil {
		return 0, internal("prune alerts", err)
	}

	subs, err := s.Engine.ListActiveUserSubscriptions()
	if err != nil {
		return 0, internal("list active subscriptions", err)
	}
	if len(subs) == 0 {
		return 0, nil
	}

	windowStart := nowNs - settings.windowNs
	if windowStart < 0 {
		windowStart = 0
	}
	rows, err := s.Engine.ListUsageDailyRange(windowStart, nowNs)
	if err != nil {
		return 0, internal("list usage", err)
	}
	usedByUser := make(map[string]int64, len(rows))
	for _, row := range rows {
		usedByUser[row.UserID] += row.IngressBytes + row.EgressBytes
	}

	openAlerts, err := s.Engine.ListAlerts(0, windowStart, true)
	if err != nil {
		return 0, internal("list alerts", err)
	}
	open := make(map[alertSubject]bool, len(openAlerts))
	for _, row := range openAlerts {
		open[alertSubject{subscriptionID: row.SubscriptionID, kind: row.Kind}] = true
	}

	plans, err := s.planDirectory()
	if err != nil {
		return 0, err
	}

	created := 0
	for _, sub := range subs {
		quota, err := alertQuotaBytes(sub, plans)
		if err != nil {
			return created, err
		}
		if quota <= 0 {
			continue
		}
		used := usedByUser[sub.UserID]
		// The comparison is float on purpose: the ratio is a fraction, and an
		// integer threshold would round every ratio below one byte to nothing.
		if float64(used) < settings.ratio*float64(quota) {
			continue
		}
		subject := alertSubject{subscriptionID: sub.ID, kind: model.AlertKindUsageAnomaly}
		if open[subject] {
			continue
		}

		id, err := newID("alrt_")
		if err != nil {
			return created, internal("generate alert id", err)
		}
		alert := model.Alert{
			ID:             id,
			Kind:           subject.kind,
			UserID:         sub.UserID,
			PlanID:         sub.PlanID,
			SubscriptionID: sub.ID,
			WindowMinutes:  settings.windowMinutes,
			ThresholdRatio: settings.ratio,
			UsedBytes:      used,
			QuotaBytes:     quota,
			DetectedAtNs:   nowNs,
		}
		if _, err := s.Engine.InsertAlert(alert); err != nil {
			return created, internal("insert alert", err)
		}
		// A subscription is listed once, so this only matters for a list that
		// somehow repeats one: the second copy must not write a second alert.
		open[subject] = true
		created++
	}
	return created, nil
}

// ListAlerts returns the alert log, newest first, with usernames and plan names
// filled in.
//
// A limit of 0 means the shipped page size and anything above the ceiling is
// clamped to it. sinceNs of 0 means the whole log; unacknowledged keeps only the
// alerts still waiting for an operator.
func (s *ControlPlaneService) ListAlerts(limit int, sinceNs int64, unacknowledged bool) ([]AlertView, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("no state engine", nil)
	}
	if sinceNs < 0 {
		return nil, invalidArg("since_ns: must not be negative")
	}
	rows, err := s.Engine.ListAlerts(clampAlertLimit(limit), sinceNs, unacknowledged)
	if err != nil {
		return nil, internal("list alerts", err)
	}
	return s.alertViews(rows)
}

// AcknowledgeAlert marks one alert as read by an operator and returns it.
//
// Acknowledging twice is refused rather than repeated: the row already carries
// the timestamp of whoever read it first, and a second write would overwrite that
// with a later one. That refusal is also what the caller turns into a "not found"
// answer, because from the console's side the alert is no longer open.
func (s *ControlPlaneService) AcknowledgeAlert(id string, nowNs int64) (AlertView, error) {
	if s == nil || s.Engine == nil {
		return AlertView{}, internal("no state engine", nil)
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return AlertView{}, invalidArg("id: must not be empty")
	}
	if nowNs <= 0 {
		return AlertView{}, invalidArg("now_ns: must be positive")
	}

	row, err := s.Engine.AcknowledgeAlert(id, nowNs)
	if errors.Is(err, state.ErrNotFound) {
		return AlertView{}, notFound("alert not found")
	}
	if err != nil {
		return AlertView{}, internal("acknowledge alert", err)
	}
	views, err := s.alertViews([]model.Alert{row})
	if err != nil {
		return AlertView{}, err
	}
	return views[0], nil
}

// alertViews resolves the names an alert row does not carry.
//
// An alert outlives the account and the plan it names, so a missing username or
// plan name is not an error: the row keeps the ids and the numbers it was
// written with, and that is what the console shows.
func (s *ControlPlaneService) alertViews(rows []model.Alert) ([]AlertView, error) {
	users, err := s.userDirectory()
	if err != nil {
		return nil, err
	}
	plans, err := s.planDirectory()
	if err != nil {
		return nil, err
	}

	views := make([]AlertView, 0, len(rows))
	for _, row := range rows {
		view := AlertView{
			ID:               row.ID,
			Kind:             row.Kind,
			UserID:           row.UserID,
			PlanID:           row.PlanID,
			WindowMinutes:    row.WindowMinutes,
			ThresholdRatio:   row.ThresholdRatio,
			UsedBytes:        row.UsedBytes,
			QuotaBytes:       row.QuotaBytes,
			DetectedAtNs:     row.DetectedAtNs,
			AcknowledgedAtNs: row.AcknowledgedAtNs,
			Note:             row.Note,
		}
		if user, ok := users[row.UserID]; ok {
			view.Username = user.Username
		}
		if plan, ok := plans[row.PlanID]; ok {
			view.PlanName = plan.Name
		}
		views = append(views, view)
	}
	return views, nil
}

// alertQuotaBytes returns the traffic budget of a subscription's plan.
//
// The live plan wins when it still exists - an operator who edits a quota expects
// the next scan to use the new number - and the subscription's own snapshot
// answers when the plan is gone, which is the order the subscription views
// resolve the plan name in. A plan with no traffic limit answers 0, which the
// scan reads as "never alert".
func alertQuotaBytes(sub model.UserSubscription, plans map[string]model.Plan) (int64, error) {
	if plan, ok := plans[sub.PlanID]; ok {
		return plan.TrafficBytes, nil
	}
	plan, err := planFromSnapshotJSON(sub.PlanSnapshotJSON)
	if err != nil {
		return 0, internal("read plan snapshot", err)
	}
	return plan.TrafficBytes, nil
}

// alertSettings resolves the alert settings from the live runtime config.
//
// Each value falls back to the shipped one when it cannot be acted on, which is
// what a runtime config persisted before the settings existed decodes to: the
// scan then reports at the shipped pace instead of refusing to run.
func (s *ControlPlaneService) alertSettings() alertScanSettings {
	cfg := s.runtimeConfig()
	windowMinutes := config.AlertWindow(cfg.AlertWindowMinutes)
	retentionDays := config.RetentionDays(cfg.AlertRetentionDays, config.DefaultAlertRetentionDays)
	return alertScanSettings{
		ratio:         config.AlertRatio(cfg.AlertUsageRatio),
		windowMinutes: windowMinutes,
		windowNs:      int64(windowMinutes) * int64(time.Minute),
		retentionNs:   int64(retentionDays) * int64(24*time.Hour),
	}
}

// runtimeConfig returns the live runtime config, or the shipped defaults when
// none is loaded yet. The scan runs on a timer that can fire before wiring
// finished, and a missing pointer is not worth a panic over a usage threshold.
func (s *ControlPlaneService) runtimeConfig() *config.RuntimeConfig {
	if s != nil && s.RuntimeCfg != nil {
		if cfg := s.RuntimeCfg.Load(); cfg != nil {
			return cfg
		}
	}
	return config.NewDefaultRuntimeConfig()
}

// clampAlertLimit applies the bounds every alert read shares.
func clampAlertLimit(limit int) int {
	if limit <= 0 {
		return defaultAlertLimit
	}
	if limit > maxAlertLimit {
		return maxAlertLimit
	}
	return limit
}
