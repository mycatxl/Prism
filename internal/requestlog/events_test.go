package requestlog

import (
	"testing"
	"time"

	"prism/internal/proxy"
)

// WP10 §3: routing advisories such as rotation_fallback_same_ip must survive the
// request-log round trip so the event is visible on the log API.
func TestRepo_RoundTripsRoutingEvents(t *testing.T) {
	repo := NewRepo(t.TempDir(), 1<<20, 2)
	if err := repo.Open(); err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ts := time.Now().Add(-time.Minute).UnixNano()
	entries := []proxy.RequestLogEntry{
		{
			ID:          "log-events",
			StartedAtNs: ts,
			ProxyType:   proxy.ProxyTypeForward,
			PlatformID:  "plat-1",
			Account:     "acct-a",
			HTTPStatus:  200,
			Events:      []string{"rotation_fallback_same_ip"},
		},
		{
			ID:          "log-no-events",
			StartedAtNs: ts - 1,
			ProxyType:   proxy.ProxyTypeForward,
			PlatformID:  "plat-2",
			Account:     "acct-b",
			HTTPStatus:  200,
		},
	}
	if _, err := repo.InsertBatch(entries); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}

	row, err := repo.GetByID("log-events")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if row == nil {
		t.Fatal("GetByID returned no row")
	}
	if len(row.Events) != 1 || row.Events[0] != "rotation_fallback_same_ip" {
		t.Fatalf("events = %v, want [rotation_fallback_same_ip]", row.Events)
	}

	empty, err := repo.GetByID("log-no-events")
	if err != nil {
		t.Fatalf("GetByID(no events): %v", err)
	}
	if empty == nil {
		t.Fatal("GetByID(no events) returned no row")
	}
	if len(empty.Events) != 0 {
		t.Fatalf("events = %v, want none", empty.Events)
	}

	rows, _, _, err := repo.List(ListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.ID == "log-events" {
			found = true
			if len(r.Events) != 1 || r.Events[0] != "rotation_fallback_same_ip" {
				t.Fatalf("listed events = %v, want [rotation_fallback_same_ip]", r.Events)
			}
		}
	}
	if !found {
		t.Fatal("listed rows do not contain log-events")
	}
}

func TestUnmarshalLogEvents_IgnoresMalformedPayload(t *testing.T) {
	if got := unmarshalLogEvents(""); got != nil {
		t.Fatalf("empty payload = %v, want nil", got)
	}
	if got := unmarshalLogEvents("{not-json"); got != nil {
		t.Fatalf("malformed payload = %v, want nil", got)
	}
	got := unmarshalLogEvents(`["rotation_fallback_same_ip"]`)
	if len(got) != 1 || got[0] != "rotation_fallback_same_ip" {
		t.Fatalf("decoded events = %v", got)
	}
}
