package store

import (
	"context"
	"database/sql"
	"net/netip"
	"strings"
	"testing"
)

func TestRecordEgress_TracksAddressChangesInHistory(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	hash := "node-one"
	v4 := netip.MustParseAddr("198.51.100.7")
	v6 := netip.MustParseAddr("2001:db8::7")

	row, changed, err := st.RecordEgress(ctx, EgressObservation{
		NodeHash: hash, IPv4: v4, IPv6: v6, V6Checked: true, Colo: "NRT", Loc: "JP", NowNs: 1000,
	})
	if err != nil {
		t.Fatalf("RecordEgress: %v", err)
	}
	if !changed {
		t.Fatal("first observation must report a change")
	}
	if row.IPv4 != v4.String() || row.IPv6 != v6.String() || row.Colo != "NRT" || row.Loc != "JP" {
		t.Fatalf("row = %+v", row)
	}

	// Re-observing the same addresses is not a change and adds no history.
	_, changed, err = st.RecordEgress(ctx, EgressObservation{
		NodeHash: hash, IPv4: v4, IPv6: v6, V6Checked: true, Colo: "NRT", Loc: "JP", NowNs: 2000,
	})
	if err != nil {
		t.Fatalf("RecordEgress: %v", err)
	}
	if changed {
		t.Fatal("identical observation reported a change")
	}

	// New IPv4 => one history row.
	newV4 := netip.MustParseAddr("203.0.113.9")
	row, changed, err = st.RecordEgress(ctx, EgressObservation{
		NodeHash: hash, IPv4: newV4, V6Checked: false, NowNs: 3000,
	})
	if err != nil {
		t.Fatalf("RecordEgress: %v", err)
	}
	if !changed || row.IPv4 != newV4.String() {
		t.Fatalf("change not applied: changed=%v row=%+v", changed, row)
	}
	if row.Colo != "NRT" || row.Loc != "JP" {
		t.Fatal("colo/loc must be preserved when the observation omits them")
	}

	// A node without IPv6 clears the address but still records the attempt.
	row, changed, err = st.RecordEgress(ctx, EgressObservation{
		NodeHash: hash, V6Checked: true, NowNs: 4000,
	})
	if err != nil {
		t.Fatalf("RecordEgress: %v", err)
	}
	if !changed {
		t.Fatal("losing IPv6 must be reported as a change")
	}
	if row.IPv6 != "" || row.V6CheckedNs != 4000 {
		t.Fatalf("row = %+v, want empty v6 and v6_checked_ns=4000", row)
	}

	history, err := st.ListEgressHistory(ctx, hash, 20)
	if err != nil {
		t.Fatalf("ListEgressHistory: %v", err)
	}
	// v6 first observation, v4 first observation, v4 change (the IPv6 loss is
	// not an address and must not add a row).
	if len(history) != 3 {
		t.Fatalf("history rows = %d, want 3: %+v", len(history), history)
	}
	if history[0].IP != newV4.String() || history[0].Family != FamilyV4 {
		t.Fatalf("newest history = %+v", history[0])
	}
	if history[1].IP != v6.String() || history[1].Family != FamilyV6 {
		t.Fatalf("second history = %+v", history[1])
	}
	if history[2].IP != v4.String() || history[2].Family != FamilyV4 {
		t.Fatalf("oldest history = %+v", history[2])
	}
}

func TestRecordEgress_Validation(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	if _, _, err := st.RecordEgress(ctx, EgressObservation{NowNs: 1}); err == nil {
		t.Fatal("expected error for missing node hash")
	}
	if _, _, err := st.RecordEgress(ctx, EgressObservation{NodeHash: "h"}); err == nil {
		t.Fatal("expected error for missing now_ns")
	}
}

func TestListNodesByIP_IsCapped(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	ip := "198.51.100.44"
	for _, hash := range []string{"a", "b", "c"} {
		if err := st.UpsertNodeEgress(ctx, NodeEgress{NodeHash: hash, IPv4: ip}); err != nil {
			t.Fatalf("UpsertNodeEgress: %v", err)
		}
	}
	if err := st.UpsertNodeEgress(ctx, NodeEgress{NodeHash: "v6-only", IPv6: ip}); err != nil {
		t.Fatalf("UpsertNodeEgress: %v", err)
	}

	all, err := st.ListNodesByIP(ctx, ip, 100)
	if err != nil {
		t.Fatalf("ListNodesByIP: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("nodes = %v, want 4", all)
	}

	capped, err := st.ListNodesByIP(ctx, ip, 2)
	if err != nil {
		t.Fatalf("ListNodesByIP: %v", err)
	}
	if len(capped) != 2 {
		t.Fatalf("capped = %v, want 2", capped)
	}
}

func TestEvidence_CRUDAndBounds(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()

	huge := strings.Repeat("x", MaxNormalizedJSONBytes+512)
	raw := strings.Repeat("y", MaxRawJSONBytes+512)
	err := st.UpsertEvidence(ctx, Evidence{
		IP: "198.51.100.5", Provider: "proxycheck", Profile: "proxycheck-v3-2",
		Status: StatusOk, ObservedAtNs: 100, ValidUntilNs: 200,
		NormalizedJSON: huge, RawJSON: sql.NullString{String: raw, Valid: true},
	})
	if err != nil {
		t.Fatalf("UpsertEvidence: %v", err)
	}

	got, ok, err := st.GetEvidence(ctx, "198.51.100.5", "proxycheck")
	if err != nil || !ok {
		t.Fatalf("GetEvidence: ok=%v err=%v", ok, err)
	}
	if len(got.NormalizedJSON) > MaxNormalizedJSONBytes {
		t.Fatalf("normalized json = %d bytes, want <= %d", len(got.NormalizedJSON), MaxNormalizedJSONBytes)
	}
	if !strings.Contains(got.NormalizedJSON, `"truncated":true`) {
		t.Fatalf("normalized json not marked truncated: %q", got.NormalizedJSON)
	}
	if len(got.Raw()) > MaxRawJSONBytes {
		t.Fatalf("raw json = %d bytes, want <= %d", len(got.Raw()), MaxRawJSONBytes)
	}

	// Upsert replaces the same (ip, provider) pair.
	if err := st.UpsertEvidence(ctx, Evidence{
		IP: "198.51.100.5", Provider: "proxycheck", Status: StatusError,
		NormalizedJSON: `{"error":"x"}`, ErrorCode: "PROVIDER_LIMIT",
		ObservedAtNs: 300, ValidUntilNs: 400,
	}); err != nil {
		t.Fatalf("UpsertEvidence replace: %v", err)
	}
	list, err := st.ListEvidenceByIP(ctx, "198.51.100.5")
	if err != nil {
		t.Fatalf("ListEvidenceByIP: %v", err)
	}
	if len(list) != 1 || list[0].Status != StatusError || list[0].ErrorCode != "PROVIDER_LIMIT" {
		t.Fatalf("list = %+v", list)
	}
	if list[0].RawJSON.Valid {
		t.Fatal("raw payload must be cleared when the replacement has none")
	}

	valid, err := st.HasValidEvidence(ctx, "198.51.100.5", "proxycheck", 350)
	if err != nil {
		t.Fatalf("HasValidEvidence: %v", err)
	}
	if valid {
		t.Fatal("status=error must not count as valid evidence")
	}
	if err := st.UpsertEvidence(ctx, Evidence{
		IP: "198.51.100.5", Provider: "proxycheck", Status: StatusOk,
		NormalizedJSON: `{}`, ObservedAtNs: 300, ValidUntilNs: 400,
	}); err != nil {
		t.Fatalf("UpsertEvidence: %v", err)
	}
	valid, err = st.HasValidEvidence(ctx, "198.51.100.5", "proxycheck", 350)
	if err != nil {
		t.Fatalf("HasValidEvidence: %v", err)
	}
	if !valid {
		t.Fatal("valid evidence not detected")
	}

	if err := st.DeleteEvidence(ctx, "198.51.100.5", "proxycheck"); err != nil {
		t.Fatalf("DeleteEvidence: %v", err)
	}
	if _, ok, _ := st.GetEvidence(ctx, "198.51.100.5", "proxycheck"); ok {
		t.Fatal("evidence still present after delete")
	}
}

func TestEvidence_RequiresIPAndProvider(t *testing.T) {
	st := openTemp(t)
	if err := st.UpsertEvidence(context.Background(), Evidence{Provider: "proxycheck"}); err == nil {
		t.Fatal("expected error for missing ip")
	}
}

func TestNodeChecks_CRUDAndDetailBounds(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()

	detail := strings.Repeat("z", MaxDetailJSONBytes+256)
	if err := st.UpsertNodeCheck(ctx, NodeCheck{
		NodeHash: "node-1", CheckID: "chatgpt", CheckVersion: 2, EgressIP: "198.51.100.1",
		Outcome: "available", Region: "JP", DetailJSON: detail, LatencyMs: 120,
		ObservedAtNs: 10, ValidUntilNs: 20,
	}); err != nil {
		t.Fatalf("UpsertNodeCheck: %v", err)
	}
	got, ok, err := st.GetNodeCheck(ctx, "node-1", "chatgpt")
	if err != nil || !ok {
		t.Fatalf("GetNodeCheck: ok=%v err=%v", ok, err)
	}
	if len(got.DetailJSON) > MaxDetailJSONBytes {
		t.Fatalf("detail json = %d bytes", len(got.DetailJSON))
	}

	// Upsert on the same (node_hash, check_id) replaces the row.
	if err := st.UpsertNodeCheck(ctx, NodeCheck{
		NodeHash: "node-1", CheckID: "chatgpt", CheckVersion: 3, Outcome: "blocked",
		ObservedAtNs: 30, ValidUntilNs: 40,
	}); err != nil {
		t.Fatalf("UpsertNodeCheck: %v", err)
	}
	list, err := st.ListNodeChecks(ctx, "node-1")
	if err != nil {
		t.Fatalf("ListNodeChecks: %v", err)
	}
	if len(list) != 1 || list[0].Outcome != "blocked" || list[0].CheckVersion != 3 {
		t.Fatalf("list = %+v", list)
	}

	if _, err := st.ListExpiredChecks(ctx, 35, 10); err != nil {
		t.Fatalf("ListExpiredChecks: %v", err)
	}
	if all, err := st.ListAllNodeChecks(ctx); err != nil || len(all) != 1 {
		t.Fatalf("ListAllNodeChecks = %d (err %v)", len(all), err)
	}
	if err := st.UpsertNodeCheck(ctx, NodeCheck{NodeHash: "node-1"}); err == nil {
		t.Fatal("expected error for missing check_id")
	}
	if err := st.DeleteNodeCheck(ctx, "node-1", "chatgpt"); err != nil {
		t.Fatalf("DeleteNodeCheck: %v", err)
	}
	if _, ok, _ := st.GetNodeCheck(ctx, "node-1", "chatgpt"); ok {
		t.Fatal("check still present after delete")
	}
}

func TestAssessments_NullableColumnsRoundTrip(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()

	if err := st.UpsertAssessment(ctx, Assessment{
		IP: "198.51.100.30", Profile: "prism-purity-v2", State: "pending", Verdict: "pending",
		PurityScore: sql.NullInt64{}, PurityBand: "unknown", Confidence: "none",
		IPType: "unknown", Native: sql.NullInt64{}, ASN: sql.NullInt64{},
		ReasonsJSON: "[]", ComponentsJSON: "[]", ComputedAtNs: 1, ValidUntilNs: 2,
	}); err != nil {
		t.Fatalf("UpsertAssessment: %v", err)
	}
	got, ok, err := st.GetAssessment(ctx, "198.51.100.30")
	if err != nil || !ok {
		t.Fatalf("GetAssessment: ok=%v err=%v", ok, err)
	}
	if got.ScoreOrNil() != nil || got.NativeOrNil() != nil || got.ASNOrNil() != nil {
		t.Fatalf("nullable columns should stay NULL: %+v", got)
	}

	if err := st.UpsertAssessment(ctx, Assessment{
		IP: "198.51.100.30", Profile: "prism-purity-v2", State: "valid", Verdict: "favorable",
		PurityScore: sql.NullInt64{Int64: 92, Valid: true}, PurityBand: "clean",
		Confidence: "high", Coverage: 0.75, IPType: "residential",
		Native: sql.NullInt64{Int64: 1, Valid: true}, Flags: 4,
		ASN: sql.NullInt64{Int64: 64500, Valid: true}, ASOrg: "Example",
		Country: "JP", City: "Tokyo", ReasonsJSON: `["NATIVE_HEURISTIC"]`,
		ComponentsJSON: `[{"source":"proxycheck"}]`, ComputedAtNs: 10, ValidUntilNs: 20,
	}); err != nil {
		t.Fatalf("UpsertAssessment: %v", err)
	}
	got, _, err = st.GetAssessment(ctx, "198.51.100.30")
	if err != nil {
		t.Fatalf("GetAssessment: %v", err)
	}
	if score := got.ScoreOrNil(); score == nil || *score != 92 {
		t.Fatalf("score = %v, want 92", score)
	}
	if native := got.NativeOrNil(); native == nil || *native != 1 {
		t.Fatalf("native = %v, want 1", native)
	}
	if got.ASNOrNil() == nil || *got.ASNOrNil() != 64500 {
		t.Fatalf("asn = %v", got.ASNOrNil())
	}

	count, err := st.CountAssessments(ctx)
	if err != nil || count != 1 {
		t.Fatalf("CountAssessments = %d (err %v)", count, err)
	}
	if err := st.UpsertAssessment(ctx, Assessment{}); err == nil {
		t.Fatal("expected error for missing ip")
	}

	// Another row so pagination and ordering are meaningful.
	if err := st.UpsertAssessment(ctx, Assessment{
		IP: "203.0.113.30", Profile: "prism-purity-v1", State: "valid", Verdict: "review",
		PurityBand: "mixed", Confidence: "low", IPType: "datacenter",
		ReasonsJSON: "[]", ComponentsJSON: "[]", ComputedAtNs: 5, ValidUntilNs: 6,
	}); err != nil {
		t.Fatalf("UpsertAssessment: %v", err)
	}
	page, err := st.ListAssessments(ctx, 1, 1)
	if err != nil || len(page) != 1 {
		t.Fatalf("ListAssessments = %d (err %v)", len(page), err)
	}
	if page[0].IP != "203.0.113.30" {
		t.Fatalf("second page ip = %s", page[0].IP)
	}
	foreign, err := st.ListAssessmentsWithForeignProfile(ctx, "prism-purity-v2", 10)
	if err != nil || len(foreign) != 1 || foreign[0].Profile != "prism-purity-v1" {
		t.Fatalf("ListAssessmentsWithForeignProfile = %+v (err %v)", foreign, err)
	}
	deferred, err := st.ListExpiredAssessments(ctx, 0, 10, 10)
	if err != nil || len(deferred) != 1 || deferred[0].IP != "203.0.113.30" {
		t.Fatalf("ListExpiredAssessments = %+v (err %v)", deferred, err)
	}
	if err := st.DeleteAssessment(ctx, "203.0.113.30"); err != nil {
		t.Fatalf("DeleteAssessment: %v", err)
	}
	if _, ok, _ := st.GetAssessment(ctx, "203.0.113.30"); ok {
		t.Fatal("assessment still present after delete")
	}
}

func TestDeleteNodeData_RemovesEveryNodeRow(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()

	if err := st.UpsertNodeEgress(ctx, NodeEgress{NodeHash: "gone", IPv4: "198.51.100.60"}); err != nil {
		t.Fatalf("UpsertNodeEgress: %v", err)
	}
	if err := st.UpsertEvidence(ctx, Evidence{
		IP: "198.51.100.60", Provider: "ippure", ViaNodeHash: "gone", Status: StatusOk,
		NormalizedJSON: "{}",
	}); err != nil {
		t.Fatalf("UpsertEvidence: %v", err)
	}
	if err := st.UpsertNodeCheck(ctx, NodeCheck{NodeHash: "gone", CheckID: "netflix", Outcome: "available"}); err != nil {
		t.Fatalf("UpsertNodeCheck: %v", err)
	}

	deleted, err := st.DeleteNodeData(ctx, []string{"gone"})
	if err != nil {
		t.Fatalf("DeleteNodeData: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("deleted = %d, want 3", deleted)
	}
	if n, err := st.DeleteNodeData(ctx, nil); err != nil || n != 0 {
		t.Fatalf("empty delete = %d (err %v)", n, err)
	}
}

func TestListNodeHashes(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	for _, hash := range []string{"b", "a"} {
		if err := st.UpsertNodeEgress(ctx, NodeEgress{NodeHash: hash}); err != nil {
			t.Fatalf("UpsertNodeEgress: %v", err)
		}
	}
	hashes, err := st.ListNodeHashes(ctx)
	if err != nil {
		t.Fatalf("ListNodeHashes: %v", err)
	}
	if len(hashes) != 2 || hashes[0] != "a" || hashes[1] != "b" {
		t.Fatalf("hashes = %v", hashes)
	}
}
