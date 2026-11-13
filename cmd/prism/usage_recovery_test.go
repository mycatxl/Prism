package main

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"prism/internal/keys"
	"prism/internal/model"
	"prism/internal/state"
	"prism/internal/usage"
)

func TestOfflineUsageRecoveryReconcilesObservedBytesOnce(t *testing.T) {
	root := t.TempDir()
	initEnv(t, root)
	if err := runInitCommand([]string{"--dir", root}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	sd, cd := filepath.Join(root, "state"), filepath.Join(root, "cache")
	engine, close, err := state.PersistenceBootstrap(sd, cd)
	if err != nil {
		t.Fatal(err)
	}
	user, err := engine.GetUserByName("admin")
	if err != nil {
		t.Fatal(err)
	}
	sub := model.UserSubscription{ID: "recovery-sub", UserID: user.ID, PlanSnapshotJSON: `{}`, PeriodStartNs: time.Now().UnixNano(), Status: model.SubscriptionStatusActive}
	if err = engine.UpsertUserSubscription(sub); err != nil {
		t.Fatal(err)
	}
	counters := usage.NewCounters()
	counters.SeedFromSubscriptions([]model.UserSubscription{sub}, 0)
	journal, err := usage.NewJournal(engine, nil)
	if err != nil {
		t.Fatal(err)
	}
	gate := usage.Gate{Journal: journal, Limiter: usage.NewLimiter(counters)}
	key := &keys.KeyState{KeyID: "test-key", UserID: user.ID, Plan: keys.PlanSnapshot{TrafficBytes: 100}}
	n, _ := gate.ReserveTraffic(key, "test-platform", true, 100)
	if n != 100 {
		t.Fatal("reservation refused")
	}
	// Independent destination observes a short write. Simulate loss of the
	// completion callback, retaining the durable outstanding reservation.
	var peer bytes.Buffer
	sent, err := peer.Write(bytes.Repeat([]byte{'x'}, 73))
	if err != nil || sent != 73 {
		t.Fatal("peer write")
	}
	pending, err := engine.PendingUsage()
	if err != nil || len(pending) != 1 {
		t.Fatal("missing durable reservation")
	}
	recovered, err := usage.NewJournal(engine, nil)
	if err != nil || !recovered.Blocked(user.ID) {
		t.Fatal("restarted access reopened")
	}
	close.Close()
	args := []string{"--state-dir", sd, "--cache-dir", cd, "--reservation", pending[0].ID, "--actual-bytes", "73", "--reason", "INC-peer-observed-73", "--confirm-offline"}
	for range 2 {
		if err = runUsageRecoveryCommand(args, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	engine, close, err = state.PersistenceBootstrap(sd, cd)
	if err != nil {
		t.Fatal(err)
	}
	defer close.Close()
	recovered, err = usage.NewJournal(engine, nil)
	if err != nil || recovered.Blocked(user.ID) {
		t.Fatal("reconciled access did not reopen")
	}
	got, err := engine.GetUserSubscription(sub.ID)
	if err != nil || got.PeriodUsedBytes != int64(peer.Len()) {
		t.Fatal("quota does not match observed bytes")
	}
	audit, err := engine.ListAudit(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, a := range audit {
		if a.Action == "offline.reconcile_usage" {
			count++
			if !strings.Contains(a.Detail, `"actual_bytes":73`) {
				t.Fatal("audit lacks verified amount")
			}
		}
	}
	if count != 1 {
		t.Fatal("replay duplicated reconciliation")
	}
}
