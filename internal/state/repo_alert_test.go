package state

import (
	"errors"
	"testing"

	"prism/internal/model"
)

func alertFixture(id string, detectedAtNs int64) model.Alert {
	return model.Alert{
		ID:             id,
		Kind:           model.AlertKindUsageAnomaly,
		UserID:         "u_alert",
		PlanID:         "plan_alert",
		SubscriptionID: "sub_alert",
		WindowMinutes:  60,
		ThresholdRatio: 0.3,
		UsedBytes:      335544320,
		QuotaBytes:     1073741824,
		DetectedAtNs:   detectedAtNs,
		Note:           "seeded by the repo test",
	}
}

func TestAlertRepo_InsertListAndAcknowledge(t *testing.T) {
	repo := newTestStateRepo(t)
	pinnedNow := int64(1700000000_000000000)

	// 1. InsertAlert keeps every field, and the three rows differ only in the
	// detection time and the open flag.
	oldest := alertFixture("alrt_oldest", pinnedNow-2000)
	if _, err := repo.InsertAlert(oldest); err != nil {
		t.Fatalf("InsertAlert(oldest): %v", err)
	}
	middle := alertFixture("alrt_middle", pinnedNow-1000)
	got, err := repo.InsertAlert(middle)
	if err != nil {
		t.Fatalf("InsertAlert(middle): %v", err)
	}
	if got != middle {
		t.Fatalf("InsertAlert returned %+v, want the stored row %+v", got, middle)
	}
	newest := alertFixture("alrt_newest", pinnedNow)
	newest.AcknowledgedAtNs = pinnedNow - 500
	if _, err := repo.InsertAlert(newest); err != nil {
		t.Fatalf("InsertAlert(newest): %v", err)
	}

	// A duplicate id is a conflict rather than a second row.
	if _, err := repo.InsertAlert(oldest); !errors.Is(err, ErrConflict) {
		t.Fatalf("InsertAlert duplicate id: got %v, want ErrConflict", err)
	}

	// 2. ListAlerts is newest first.
	all, err := repo.ListAlerts(0, 0, false)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 alerts, got %d", len(all))
	}
	for i, wantID := range []string{"alrt_newest", "alrt_middle", "alrt_oldest"} {
		if all[i].ID != wantID {
			t.Fatalf("alert[%d]: got %s, want %s", i, all[i].ID, wantID)
		}
	}
	if all[1].ThresholdRatio != 0.3 || all[1].UsedBytes != 335544320 || all[1].QuotaBytes != 1073741824 ||
		all[1].WindowMinutes != 60 || all[1].Note != "seeded by the repo test" {
		t.Fatalf("stored alert fields mismatched: %+v", all[1])
	}

	// 3. unacknowledged keeps only the rows still waiting for an operator.
	open, err := repo.ListAlerts(0, 0, true)
	if err != nil {
		t.Fatalf("ListAlerts(unacknowledged): %v", err)
	}
	if len(open) != 2 || open[0].ID != "alrt_middle" || open[1].ID != "alrt_oldest" {
		t.Fatalf("unacknowledged list unexpected: %+v", open)
	}
	if open[0].AcknowledgedAtNs != 0 {
		t.Fatalf("expected the open row to carry acknowledged_at_ns 0, got %d", open[0].AcknowledgedAtNs)
	}

	// 4. The time window is inclusive on the lower bound.
	window, err := repo.ListAlerts(0, pinnedNow-1000, false)
	if err != nil {
		t.Fatalf("ListAlerts(since): %v", err)
	}
	if len(window) != 2 || window[0].ID != "alrt_newest" || window[1].ID != "alrt_middle" {
		t.Fatalf("windowed list unexpected: %+v", window)
	}

	// 5. A positive limit truncates the newest rows only.
	limited, err := repo.ListAlerts(1, 0, false)
	if err != nil {
		t.Fatalf("ListAlerts(limit): %v", err)
	}
	if len(limited) != 1 || limited[0].ID != "alrt_newest" {
		t.Fatalf("limited list unexpected: %+v", limited)
	}

	// 6. AcknowledgeAlert records the read time and only once.
	acknowledged, err := repo.AcknowledgeAlert("alrt_middle", pinnedNow+1000)
	if err != nil {
		t.Fatalf("AcknowledgeAlert: %v", err)
	}
	if acknowledged.ID != "alrt_middle" || acknowledged.AcknowledgedAtNs != pinnedNow+1000 {
		t.Fatalf("AcknowledgeAlert returned %+v", acknowledged)
	}
	if _, err := repo.AcknowledgeAlert("alrt_middle", pinnedNow+2000); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second AcknowledgeAlert: got %v, want ErrNotFound", err)
	}
	if _, err := repo.AcknowledgeAlert("alrt_unknown", pinnedNow+2000); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AcknowledgeAlert on unknown id: got %v, want ErrNotFound", err)
	}

	open, err = repo.ListAlerts(0, 0, true)
	if err != nil {
		t.Fatalf("ListAlerts after acknowledge: %v", err)
	}
	if len(open) != 1 || open[0].ID != "alrt_oldest" {
		t.Fatalf("expected only alrt_oldest to stay open, got %+v", open)
	}
}

func TestAlertRepo_DeleteAlertsBefore(t *testing.T) {
	repo := newTestStateRepo(t)
	cutoff := int64(1700000000_000000000)

	for _, id := range []string{"alrt_before", "alrt_at_cutoff", "alrt_after"} {
		detectedAt := cutoff
		switch id {
		case "alrt_before":
			detectedAt = cutoff - 1
		case "alrt_after":
			detectedAt = cutoff + 1
		}
		if _, err := repo.InsertAlert(alertFixture(id, detectedAt)); err != nil {
			t.Fatalf("InsertAlert(%s): %v", id, err)
		}
	}

	// The cutoff itself is kept: only rows strictly older age out.
	removed, err := repo.DeleteAlertsBefore(cutoff)
	if err != nil {
		t.Fatalf("DeleteAlertsBefore: %v", err)
	}
	if removed != 1 {
		t.Fatalf("DeleteAlertsBefore removed %d rows, want 1", removed)
	}
	remaining, err := repo.ListAlerts(0, 0, false)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(remaining) != 2 || remaining[0].ID != "alrt_after" || remaining[1].ID != "alrt_at_cutoff" {
		t.Fatalf("delete removed the wrong rows: %+v", remaining)
	}

	// Nothing left before the cutoff means nothing to remove.
	removed, err = repo.DeleteAlertsBefore(cutoff)
	if err != nil {
		t.Fatalf("DeleteAlertsBefore (second call): %v", err)
	}
	if removed != 0 {
		t.Fatalf("second DeleteAlertsBefore removed %d rows, want 0", removed)
	}

	// A later cutoff takes the rest.
	removed, err = repo.DeleteAlertsBefore(cutoff + 10)
	if err != nil {
		t.Fatalf("DeleteAlertsBefore (final call): %v", err)
	}
	if removed != 2 {
		t.Fatalf("final DeleteAlertsBefore removed %d rows, want 2", removed)
	}
	remaining, err = repo.ListAlerts(0, 0, false)
	if err != nil {
		t.Fatalf("ListAlerts (after final delete): %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected an empty alert log, got %+v", remaining)
	}
}
