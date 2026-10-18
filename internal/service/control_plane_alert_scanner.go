package service

import (
	"sync"
	"time"

	"prism/internal/safego"
)

// UsageAlertScanInterval is how often a deployment looks for a subscription that
// crossed its alert ratio. The window the detector compares is an hour by
// default, so a scan a minute apart reports a crossing while it is still worth
// acting on and costs a bounded read of the usage rows.
const UsageAlertScanInterval = time.Minute

// StartUsageAlertScanner runs ScanUsageAlerts every interval and reports what it
// found through logf, until the returned function is called.
//
// The loop never stops on a scan error: an alerting component that dies on a
// transient database error would hide exactly the incident it exists to report,
// so a failure is logged and retried at the next tick. A cancelled scan (a
// shutdown in progress) is not an error an operator needs to see.
//
// The returned stop function is idempotent, like the other Start helpers in the
// tree (usage.Retention.Start, metrics.StartRetentionPruner,
// api.StartAuditPrunerWithRetention): stopping an already stopped scanner must be
// a no-op rather than a panic on the closed channel, which is the failure a
// shutdown path that runs twice would hit. Each call still waits for the scan
// goroutine to exit, so once stop has returned no further scan can start.
// stop has returned no further scan can start.
func StartUsageAlertScanner(cp *ControlPlaneService, interval time.Duration, logf func(string, ...any)) func() {
	if interval <= 0 {
		interval = UsageAlertScanInterval
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				// A panic inside one pass must not end the loop: the next
				// tick retries, exactly as it does after a failed pass.
				safego.Run("the usage alert scan", func() {
					created, err := cp.ScanUsageAlerts(time.Now().UnixNano())
					if err != nil {
						logf("usage alert scan failed: %v", err)
						return
					}
					if created > 0 {
						logf("usage alerts: %d new alert(s)", created)
					}
				})
			}
		}
	}()
	// close(stop) happens exactly once; the wait for done stays outside the Once,
	// so a repeated call returns only after the loop has really stopped.
	return func() {
		once.Do(func() { close(stop) })
		<-done
	}
}
