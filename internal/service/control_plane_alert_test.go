package service

import (
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/config"
	"prism/internal/model"
)

// alertGiB is the plan quota the alert tests measure against: 1 GiB, which the
// shipped 0.3 ratio turns into a threshold of 322122547.2 bytes.
const alertGiB = int64(1) << 30

// setAlertRuntimeConfig installs a runtime config on the service, which is what
// the scanner reads its ratio, window and retention from.
func setAlertRuntimeConfig(t *testing.T, cp *ControlPlaneService, cfg *config.RuntimeConfig) {
	t.Helper()
	ptr := &atomic.Pointer[config.RuntimeConfig]{}
	ptr.Store(cfg)
	cp.RuntimeCfg = ptr
}

// alertTestSubscription puts one account on a fresh plan and returns its active
// subscription plus the plan's display name.
func alertTestSubscription(
	t *testing.T,
	cp *ControlPlaneService,
	nowNs int64,
	username string,
	quotaBytes int64,
) (model.UserSubscription, string) {
	t.Helper()

	plan, err := cp.CreatePlan(PlanInput{
		Name:         "Alert Plan " + username,
		TrafficBytes: quotaBytes,
		DurationDays: 30,
		Status:       "active",
	}, nowNs)
	if err != nil {
		t.Fatalf("CreatePlan(%s): %v", username, err)
	}
	user, err := cp.CreateTenantUser(TenantUserInput{
		Username:     username,
		Password:     "password-123456",
		Role:         "user",
		PlanID:       plan.ID,
		DurationDays: 30,
	}, nowNs)
	if err != nil {
		t.Fatalf("CreateTenantUser(%s): %v", username, err)
	}
	if user.Subscription == nil {
		t.Fatalf("CreateTenantUser(%s) returned no subscription", username)
	}
	sub, err := cp.Engine.GetUserSubscription(user.Subscription.ID)
	if err != nil {
		t.Fatalf("GetUserSubscription(%s): %v", user.Subscription.ID, err)
	}
	return *sub, plan.Name
}

// seedAlertUsage adds usage to the hour that contains atNs. Repeated calls for
// the same hour accumulate, which is what the boundary test walks up byte by
// byte.
func seedAlertUsage(t *testing.T, cp *ControlPlaneService, userID, platformID string, atNs, bytes int64) {
	t.Helper()

	row := model.UsageRow{
		KeyID:        "k_" + userID,
		UserID:       userID,
		PlatformID:   platformID,
		TSNs:         atNs,
		IngressBytes: bytes / 2,
		EgressBytes:  bytes - bytes/2,
		Requests:     1,
	}
	if err := cp.Engine.UpsertUsageHourly([]model.UsageRow{row}); err != nil {
		t.Fatalf("UpsertUsageHourly: %v", err)
	}
}

// seedAlertRow writes one alert straight into the log, for the cases the
// scanner is not what is under test.
func seedAlertRow(t *testing.T, cp *ControlPlaneService, alert model.Alert) model.Alert {
	t.Helper()

	stored, err := cp.Engine.InsertAlert(alert)
	if err != nil {
		t.Fatalf("InsertAlert(%s): %v", alert.ID, err)
	}
	return stored
}

func TestUsageAlertScan_ThresholdDedupAndAcknowledge(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	setAlertRuntimeConfig(t, cp, config.NewDefaultRuntimeConfig())

	sub, planName := alertTestSubscription(t, cp, nowNs, "alert_threshold", alertGiB)
	// 320 MiB inside the last hour: above 30% of the 1 GiB quota.
	seedAlertUsage(t, cp, sub.UserID, "plt_alert", nowNs-int64(5*time.Minute), 320<<20)

	// 1. The crossing is reported exactly once.
	created, err := cp.ScanUsageAlerts(nowNs)
	if err != nil {
		t.Fatalf("ScanUsageAlerts: %v", err)
	}
	if created != 1 {
		t.Fatalf("first scan created %d alerts, want 1", created)
	}

	alerts, err := cp.ListAlerts(0, 0, false)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert in the log, got %d", len(alerts))
	}
	alert := alerts[0]
	if alert.Kind != model.AlertKindUsageAnomaly {
		t.Fatalf("alert kind: got %q, want %q", alert.Kind, model.AlertKindUsageAnomaly)
	}
	if alert.UserID != sub.UserID || alert.Username != "alert_threshold" {
		t.Fatalf("alert user: got %s/%q, want %s/alert_threshold", alert.UserID, alert.Username, sub.UserID)
	}
	if alert.PlanID != sub.PlanID || alert.PlanName != planName {
		t.Fatalf("alert plan: got %s/%q, want %s/%q", alert.PlanID, alert.PlanName, sub.PlanID, planName)
	}
	if alert.WindowMinutes != config.DefaultAlertWindowMinutes || alert.ThresholdRatio != config.DefaultAlertUsageRatio {
		t.Fatalf("alert settings: got window=%d ratio=%v, want window=%d ratio=%v",
			alert.WindowMinutes, alert.ThresholdRatio,
			config.DefaultAlertWindowMinutes, config.DefaultAlertUsageRatio)
	}
	if alert.UsedBytes != 320<<20 || alert.QuotaBytes != alertGiB {
		t.Fatalf("alert measurement: got used=%d quota=%d, want used=%d quota=%d",
			alert.UsedBytes, alert.QuotaBytes, int64(320<<20), alertGiB)
	}
	if alert.DetectedAtNs != nowNs || alert.AcknowledgedAtNs != 0 {
		t.Fatalf("alert timestamps: got detected=%d acknowledged=%d, want %d/0",
			alert.DetectedAtNs, alert.AcknowledgedAtNs, nowNs)
	}

	// 2. A second scan inside the same window does not repeat the alert.
	created, err = cp.ScanUsageAlerts(nowNs + int64(time.Minute))
	if err != nil {
		t.Fatalf("second ScanUsageAlerts: %v", err)
	}
	if created != 0 {
		t.Fatalf("second scan created %d alerts, want 0", created)
	}
	if alerts, err = cp.ListAlerts(0, 0, false); err != nil {
		t.Fatalf("ListAlerts after second scan: %v", err)
	} else if len(alerts) != 1 {
		t.Fatalf("second scan changed the log: %+v", alerts)
	}

	// 3. An operator acknowledges it.
	acknowledged, err := cp.AcknowledgeAlert(alert.ID, nowNs+int64(2*time.Minute))
	if err != nil {
		t.Fatalf("AcknowledgeAlert: %v", err)
	}
	if acknowledged.AcknowledgedAtNs != nowNs+int64(2*time.Minute) {
		t.Fatalf("acknowledged_at_ns: got %d, want %d", acknowledged.AcknowledgedAtNs, nowNs+int64(2*time.Minute))
	}
	if acknowledged.Username != "alert_threshold" || acknowledged.PlanName != planName {
		t.Fatalf("acknowledged alert lost its names: %+v", acknowledged)
	}

	// 4. Once nothing is open, the same window reports again.
	created, err = cp.ScanUsageAlerts(nowNs + int64(3*time.Minute))
	if err != nil {
		t.Fatalf("third ScanUsageAlerts: %v", err)
	}
	if created != 1 {
		t.Fatalf("scan after acknowledgement created %d alerts, want 1", created)
	}
	alerts, err = cp.ListAlerts(0, 0, false)
	if err != nil {
		t.Fatalf("ListAlerts after third scan: %v", err)
	}
	if len(alerts) != 2 || alerts[0].AcknowledgedAtNs != 0 {
		t.Fatalf("expected a new open alert in front of the acknowledged one, got %+v", alerts)
	}

	// 5. The open filter shows exactly the new one.
	open, err := cp.ListAlerts(0, 0, true)
	if err != nil {
		t.Fatalf("ListAlerts(unacknowledged): %v", err)
	}
	if len(open) != 1 || open[0].ID != alerts[0].ID {
		t.Fatalf("open alert list unexpected: %+v", open)
	}

	// 6. Acknowledging twice is refused as "not found": the alert is no longer
	// open, and the first read time is not overwritten.
	_, err = cp.AcknowledgeAlert(acknowledged.ID, nowNs+int64(4*time.Minute))
	assertServiceErrorCode(t, err, "NOT_FOUND")
	refetched, err := cp.AcknowledgeAlert(alerts[0].ID, nowNs+int64(5*time.Minute))
	if err != nil {
		t.Fatalf("AcknowledgeAlert on the new alert: %v", err)
	}
	if refetched.AcknowledgedAtNs != nowNs+int64(5*time.Minute) {
		t.Fatalf("second alert acknowledged_at_ns: got %d", refetched.AcknowledgedAtNs)
	}
}

func TestUsageAlertScan_ThresholdBoundaryAndUnlimitedPlans(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	setAlertRuntimeConfig(t, cp, config.NewDefaultRuntimeConfig())

	// 1. Just below 30% of 1 GiB - 322122547 of the 322122547.2 byte threshold -
	// the account is not reported yet.
	sub, _ := alertTestSubscription(t, cp, nowNs, "alert_below", alertGiB)
	seedAlertUsage(t, cp, sub.UserID, "plt_alert", nowNs-int64(time.Minute), 322122547)
	created, err := cp.ScanUsageAlerts(nowNs)
	if err != nil {
		t.Fatalf("ScanUsageAlerts: %v", err)
	}
	if created != 0 {
		t.Fatalf("scan below the threshold created %d alerts, want 0", created)
	}

	// 2. One byte more crosses it, which is what keeps a ratio of a fraction
	// meaningful.
	seedAlertUsage(t, cp, sub.UserID, "plt_alert", nowNs-int64(time.Minute), 1)
	created, err = cp.ScanUsageAlerts(nowNs + int64(time.Second))
	if err != nil {
		t.Fatalf("ScanUsageAlerts at the threshold: %v", err)
	}
	if created != 1 {
		t.Fatalf("scan at the threshold created %d alerts, want 1", created)
	}
	alerts, err := cp.ListAlerts(0, 0, false)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(alerts) != 1 || alerts[0].UsedBytes != 322122548 || alerts[0].QuotaBytes != alertGiB {
		t.Fatalf("threshold alert unexpected: %+v", alerts)
	}

	// 3. A plan with no traffic limit never alerts: unlimited is not a budget
	// that can be run out of.
	unlimited, _ := alertTestSubscription(t, cp, nowNs, "alert_unlimited", 0)
	seedAlertUsage(t, cp, unlimited.UserID, "plt_alert", nowNs-int64(time.Minute), 8<<30)
	created, err = cp.ScanUsageAlerts(nowNs + int64(2*time.Second))
	if err != nil {
		t.Fatalf("ScanUsageAlerts for the unlimited plan: %v", err)
	}
	if created != 0 {
		t.Fatalf("unlimited plan created %d alerts, want 0", created)
	}
	if alerts, err = cp.ListAlerts(0, 0, false); err != nil {
		t.Fatalf("ListAlerts after the unlimited scan: %v", err)
	} else if len(alerts) != 1 {
		t.Fatalf("unlimited plan changed the log: %+v", alerts)
	}
}

func TestUsageAlertScan_FollowsRuntimeConfigAndPrunes(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	cfg := config.NewDefaultRuntimeConfig()
	cfg.AlertUsageRatio = 0.5
	cfg.AlertWindowMinutes = 10
	cfg.AlertRetentionDays = 30
	setAlertRuntimeConfig(t, cp, cfg)

	sub, _ := alertTestSubscription(t, cp, nowNs, "alert_hot", alertGiB)

	// 1. 320 MiB is over the shipped 30% ratio but under the configured 50%: the
	// runtime config is what decides.
	seedAlertUsage(t, cp, sub.UserID, "plt_alert", nowNs-int64(5*time.Minute), 320<<20)
	created, err := cp.ScanUsageAlerts(nowNs)
	if err != nil {
		t.Fatalf("ScanUsageAlerts: %v", err)
	}
	if created != 0 {
		t.Fatalf("scan under the configured ratio created %d alerts, want 0", created)
	}

	// 2. Two alerts that sit on either side of the configured 30 day retention.
	seedAlertRow(t, cp, model.Alert{
		ID:             "alrt_stale",
		Kind:           model.AlertKindUsageAnomaly,
		UserID:         sub.UserID,
		SubscriptionID: "sub_stale",
		DetectedAtNs:   nowNs - int64(40*24*time.Hour),
	})
	seedAlertRow(t, cp, model.Alert{
		ID:             "alrt_recent",
		Kind:           model.AlertKindUsageAnomaly,
		UserID:         sub.UserID,
		SubscriptionID: "sub_recent",
		DetectedAtNs:   nowNs - int64(20*24*time.Hour),
	})

	// 3. Crossing half the quota reports, with the settings it used recorded on
	// the row, and the stale alert is pruned in the same call.
	seedAlertUsage(t, cp, sub.UserID, "plt_alert", nowNs-int64(4*time.Minute), 300<<20)
	created, err = cp.ScanUsageAlerts(nowNs + int64(time.Minute))
	if err != nil {
		t.Fatalf("second ScanUsageAlerts: %v", err)
	}
	if created != 1 {
		t.Fatalf("scan over the configured ratio created %d alerts, want 1", created)
	}
	alerts, err := cp.ListAlerts(0, 0, false)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(alerts) != 2 {
		t.Fatalf("expected the new alert and the recent one, got %+v", alerts)
	}
	if alerts[0].WindowMinutes != 10 {
		t.Fatalf("alert window_minutes: got %d, want the configured 10", alerts[0].WindowMinutes)
	}
	if alerts[0].ThresholdRatio != 0.5 {
		t.Fatalf("alert threshold_ratio: got %v, want the configured 0.5", alerts[0].ThresholdRatio)
	}
	if alerts[0].UsedBytes != 620<<20 {
		t.Fatalf("alert used_bytes: got %d, want %d", alerts[0].UsedBytes, int64(620<<20))
	}
	if alerts[1].ID != "alrt_recent" {
		t.Fatalf("the alert inside the retention window was pruned: %+v", alerts)
	}
}

func TestUsageAlerts_ListFiltersAndValidation(t *testing.T) {
	// No runtime config at all: every alert method must still answer with the
	// shipped settings rather than panic on the nil pointer.
	cp, _, _, nowNs := newTenantTestService(t)

	sub, planName := alertTestSubscription(t, cp, nowNs, "alert_list", alertGiB)
	newest := seedAlertRow(t, cp, model.Alert{
		ID:             "alrt_open_newest",
		Kind:           model.AlertKindUsageAnomaly,
		UserID:         sub.UserID,
		PlanID:         sub.PlanID,
		SubscriptionID: sub.ID,
		DetectedAtNs:   nowNs - 1000,
	})
	seedAlertRow(t, cp, model.Alert{
		ID:               "alrt_ack",
		Kind:             model.AlertKindUsageAnomaly,
		UserID:           sub.UserID,
		PlanID:           sub.PlanID,
		SubscriptionID:   sub.ID,
		DetectedAtNs:     nowNs - 2000,
		AcknowledgedAtNs: nowNs - 1500,
	})
	oldest := seedAlertRow(t, cp, model.Alert{
		ID:             "alrt_open_oldest",
		Kind:           model.AlertKindUsageAnomaly,
		UserID:         "u_gone",
		PlanID:         "plan_gone",
		SubscriptionID: "sub_gone",
		DetectedAtNs:   nowNs - 3000,
	})

	// 1. The whole log, newest first.
	all, err := cp.ListAlerts(0, 0, false)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(all) != 3 || all[0].ID != newest.ID || all[2].ID != oldest.ID {
		t.Fatalf("alert log unexpected: %+v", all)
	}
	if all[0].Username != "alert_list" || all[0].PlanName != planName {
		t.Fatalf("names not resolved: %+v", all[0])
	}
	// An alert outlives the account and the plan it names: the ids stay, the
	// names are empty rather than an error.
	if all[2].Username != "" || all[2].PlanName != "" {
		t.Fatalf("expected empty names for a deleted account, got %+v", all[2])
	}

	// 2. Filters.
	open, err := cp.ListAlerts(0, 0, true)
	if err != nil {
		t.Fatalf("ListAlerts(unacknowledged): %v", err)
	}
	if len(open) != 2 || open[0].ID != newest.ID || open[1].ID != oldest.ID {
		t.Fatalf("unacknowledged filter unexpected: %+v", open)
	}
	windowed, err := cp.ListAlerts(0, nowNs-2000, false)
	if err != nil {
		t.Fatalf("ListAlerts(since): %v", err)
	}
	if len(windowed) != 2 || windowed[0].ID != newest.ID || windowed[1].ID != "alrt_ack" {
		t.Fatalf("time window filter unexpected: %+v", windowed)
	}

	// 3. Bounds: a limit of 0 is the shipped page size, and anything above the
	// ceiling is clamped instead of served.
	limited, err := cp.ListAlerts(2, 0, false)
	if err != nil {
		t.Fatalf("ListAlerts(limit 2): %v", err)
	}
	if len(limited) != 2 || limited[0].ID != newest.ID {
		t.Fatalf("limited list unexpected: %+v", limited)
	}
	if clamped, err := cp.ListAlerts(maxAlertLimit+100, 0, false); err != nil {
		t.Fatalf("ListAlerts(over the ceiling): %v", err)
	} else if len(clamped) != 3 {
		t.Fatalf("clamped list unexpected: %+v", clamped)
	}

	// 4. Validation and the not-found answer.
	_, err = cp.ListAlerts(0, -1, false)
	assertServiceErrorCode(t, err, "INVALID_ARGUMENT")
	_, err = cp.AcknowledgeAlert("   ", nowNs)
	assertServiceErrorCode(t, err, "INVALID_ARGUMENT")
	_, err = cp.AcknowledgeAlert(newest.ID, 0)
	assertServiceErrorCode(t, err, "INVALID_ARGUMENT")
	_, err = cp.AcknowledgeAlert("alrt_missing", nowNs)
	assertServiceErrorCode(t, err, "NOT_FOUND")
	_, err = cp.ScanUsageAlerts(0)
	assertServiceErrorCode(t, err, "INVALID_ARGUMENT")
}

// stopMustNotPanic calls the stop function of a background loop and turns the
// panic of a repeated close - "close of closed channel" - into a test failure
// instead of crashing the test binary, so the regression is reported as a FAIL of
// the calling test.
func stopMustNotPanic(t *testing.T, stop func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("stop panicked: %v", r)
		}
	}()
	stop()
}

// TestUsageAlertScanner_StopIsIdempotent pins the scanner's stop function to the
// contract the other Start helpers in the tree keep (usage.Retention.Start,
// metrics.StartRetentionPruner, api.StartAuditPrunerWithRetention): a second call
// from a shutdown path that runs twice must be a no-op instead of a panic on the
// closed channel, and once the first call has returned the loop is really gone,
// so no further scan can run.
func TestUsageAlertScanner_StopIsIdempotent(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	setAlertRuntimeConfig(t, cp, config.NewDefaultRuntimeConfig())

	sub, _ := alertTestSubscription(t, cp, nowNs, "alert_scanner", alertGiB)
	// The scanner reads the wall clock, so the crossing has to sit inside the
	// last hour of the wall clock, not next to the fixture's fixed now.
	seedAlertUsage(t, cp, sub.UserID, "plt_alert", time.Now().UnixNano()-int64(5*time.Minute), 320<<20)

	// alertCount is the size of the log.
	alertCount := func() int {
		alerts, err := cp.ListAlerts(0, 0, false)
		if err != nil {
			t.Fatalf("ListAlerts: %v", err)
		}
		return len(alerts)
	}
	// acknowledgeOpenAlerts keeps the log a moving target: the scan refuses to
	// repeat an alert that is still open inside its window, so without this a
	// stopped loop and a running one would look the same.
	acknowledgeOpenAlerts := func() {
		alerts, err := cp.ListAlerts(0, 0, true)
		if err != nil {
			t.Fatalf("ListAlerts(unacknowledged): %v", err)
		}
		now := time.Now().UnixNano()
		for _, alert := range alerts {
			if _, err := cp.AcknowledgeAlert(alert.ID, now); err != nil {
				t.Fatalf("AcknowledgeAlert(%s): %v", alert.ID, err)
			}
		}
	}

	const interval = 5 * time.Millisecond
	stop := StartUsageAlertScanner(cp, interval, nil)
	deadline := time.Now().Add(5 * time.Second)

	// 1. The loop reports the crossing on its own, with no manual scan.
	for alertCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(interval)
	}
	if alertCount() == 0 {
		t.Fatal("the scanner never reported the crossing")
	}

	// 2. The loop is alive: once the open alert is acknowledged, the next tick
	// records the crossing again.
	acknowledgeOpenAlerts()
	for alertCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(interval)
	}
	if got := alertCount(); got < 2 {
		t.Fatalf("the scanner stopped scanning while it was running: %d alerts, want 2", got)
	}

	// 3. Stopping twice must not panic, and both calls must return: the first
	// waits for the scan goroutine, the second is a no-op on the closed channel.
	for call := 1; call <= 2; call++ {
		startedAt := time.Now()
		stopMustNotPanic(t, stop)
		if elapsed := time.Since(startedAt); elapsed > 2*time.Second {
			t.Fatalf("stop call %d took %s, want a prompt return", call, elapsed)
		}
	}

	// 4. Nothing scans after the stop returned: the open alert is acknowledged
	// again, so a scan during the wait would show up as a new alert.
	acknowledgeOpenAlerts()
	before := alertCount()
	time.Sleep(20 * interval)
	if after := alertCount(); after != before {
		t.Fatalf("the scanner kept running after stop: %d alerts, want %d", after, before)
	}
}
