package intel

import (
	"context"
	"database/sql"
	"net/netip"
	"path/filepath"
	"testing"

	"prism/internal/intel/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state", store.FileName))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestCodeTables_RoundTrip(t *testing.T) {
	for name, code := range map[string]uint8{
		"excellent": BandExcellent, "clean": BandClean, "fair": BandFair,
		"mixed": BandMixed, "poor": BandPoor, "unknown": BandUnknown,
	} {
		if got := BandCode(name); got != code {
			t.Fatalf("BandCode(%q) = %d, want %d", name, got, code)
		}
		if got := BandName(code); got != name {
			t.Fatalf("BandName(%d) = %q, want %q", code, got, name)
		}
	}
	for name, code := range map[string]uint8{
		"none": ConfidenceNone, "low": ConfidenceLow, "medium": ConfidenceMedium, "high": ConfidenceHigh,
	} {
		if ConfidenceCode(name) != code || ConfidenceName(code) != name {
			t.Fatalf("confidence round-trip failed for %q", name)
		}
	}
	for name, code := range map[string]uint8{
		"pending": VerdictPending, "favorable": VerdictFavorable, "caution": VerdictCaution,
		"incomplete": VerdictIncomplete, "review": VerdictReview,
		"conflicting": VerdictConflicting, "high_risk": VerdictHighRisk,
	} {
		if VerdictCode(name) != code || VerdictName(code) != name {
			t.Fatalf("verdict round-trip failed for %q", name)
		}
	}
	for name, code := range map[string]uint8{
		"unknown": IPTypeUnknown, "residential": IPTypeResidential, "mobile": IPTypeMobile,
		"business": IPTypeBusiness, "wireless": IPTypeWireless, "datacenter": IPTypeDatacenter,
		"non_residential": IPTypeNonResidential, "conflicting": IPTypeConflicting,
	} {
		if IPTypeCode(name) != code || IPTypeName(code) != name {
			t.Fatalf("ip type round-trip failed for %q", name)
		}
	}
	for name, code := range map[string]uint8{
		"unknown": OutcomeUnknown, "available": OutcomeAvailable, "blocked": OutcomeBlocked,
		"region_limited": OutcomeRegionLimited, "captcha": OutcomeCaptcha, "error": OutcomeError,
	} {
		if OutcomeCode(name) != code || OutcomeName(code) != name {
			t.Fatalf("outcome round-trip failed for %q", name)
		}
	}
	// Unknown names fall back to the zero code instead of panicking.
	if BandCode("bogus") != BandUnknown || VerdictName(200) != "pending" {
		t.Fatal("unknown values must fall back to the zero code")
	}
}

func TestFlags_NamesRoundTrip(t *testing.T) {
	flags := FlagCompromised | FlagTor | FlagDNSBL
	names := FlagNames(flags)
	if len(names) != 3 {
		t.Fatalf("names = %v", names)
	}
	var rebuilt uint16
	for _, name := range names {
		rebuilt |= FlagFromName(name)
	}
	if rebuilt != flags {
		t.Fatalf("rebuilt = %d, want %d", rebuilt, flags)
	}
	if FlagFromName("nope") != 0 {
		t.Fatal("unknown flag name must map to 0")
	}
}

func TestAssessmentLite_UnknownAndReadback(t *testing.T) {
	row := store.Assessment{
		IP: "198.51.100.1", Profile: "prism-purity-v2", State: "pending", Verdict: "pending",
		PurityBand: "unknown", Confidence: "none", IPType: "unknown",
		ComputedAtNs: 10, ValidUntilNs: 20,
	}
	lite := AssessmentLiteFromRow(row)
	if lite.ScoreValue() != nil || lite.NativeValue() != nil {
		t.Fatalf("unknown values must stay nil: %+v", lite)
	}
	if lite.Valid(19) != true || lite.Valid(20) != false {
		t.Fatal("Valid must be exclusive of valid_until")
	}

	row.PurityScore = sql.NullInt64{Int64: 88, Valid: true}
	row.Native = sql.NullInt64{Int64: 0, Valid: true}
	row.Flags = int64(FlagProxy)
	lite = AssessmentLiteFromRow(row)
	if score := lite.ScoreValue(); score == nil || *score != 88 {
		t.Fatalf("score = %v", score)
	}
	if native := lite.NativeValue(); native == nil || *native {
		t.Fatalf("native = %v, want false", native)
	}
	if lite.Flags != FlagProxy {
		t.Fatalf("flags = %d", lite.Flags)
	}
}

func TestSnapshot_RebuildsIdenticalProjectionAfterRestart(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if err := st.UpsertAssessment(ctx, store.Assessment{
		IP: "198.51.100.7", Profile: "prism-purity-v2", State: "valid", Verdict: "review",
		PurityScore: sql.NullInt64{Int64: 71, Valid: true}, PurityBand: "mixed",
		Confidence: "medium", Coverage: 0.5, IPType: "datacenter",
		Flags: int64(FlagProxy | FlagHosting), ASOrg: "Example", Country: "JP",
		ReasonsJSON: "[]", ComponentsJSON: "[]", ComputedAtNs: 5, ValidUntilNs: 100,
	}); err != nil {
		t.Fatalf("UpsertAssessment: %v", err)
	}
	if err := st.UpsertNodeCheck(ctx, store.NodeCheck{
		NodeHash: "node-1", CheckID: "chatgpt", CheckVersion: 2, EgressIP: "198.51.100.7",
		Outcome: "available", Region: "JP", DetailJSON: "{}", ObservedAtNs: 6, ValidUntilNs: 90,
	}); err != nil {
		t.Fatalf("UpsertNodeCheck: %v", err)
	}

	before := NewSnapshot()
	if err := before.LoadFrom(ctx, st); err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	assessments, checks := before.Len()
	if assessments != 1 || checks != 1 {
		t.Fatalf("Len = %d/%d, want 1/1", assessments, checks)
	}

	// "Restart": a brand new projection reading the same database.
	after := NewSnapshot()
	if err := after.LoadFrom(ctx, st); err != nil {
		t.Fatalf("LoadFrom after restart: %v", err)
	}

	ip := netip.MustParseAddr("198.51.100.7")
	first, ok1 := before.Assessment(ip)
	second, ok2 := after.Assessment(ip)
	if !ok1 || !ok2 || first != second {
		t.Fatalf("assessment projection differs: %+v vs %+v", first, second)
	}
	checkFirst, ok1 := before.CheckOutcome("node-1", "chatgpt")
	checkSecond, ok2 := after.CheckOutcome("node-1", "chatgpt")
	if !ok1 || !ok2 || checkFirst != checkSecond {
		t.Fatalf("check projection differs: %+v vs %+v", checkFirst, checkSecond)
	}
	if outcome := OutcomeName(checkFirst.Outcome); outcome != "available" {
		t.Fatalf("outcome = %q", outcome)
	}
}

func TestSnapshot_IncrementalUpdates(t *testing.T) {
	snap := NewSnapshot()
	ip := netip.MustParseAddr("203.0.113.5")

	if _, ok := snap.Assessment(ip); ok {
		t.Fatal("empty snapshot must not report an assessment")
	}
	snap.SetAssessment(ip, AssessmentLite{Score: 90, Band: BandClean, Verdict: VerdictFavorable})
	lite, ok := snap.Assessment(ip)
	if !ok || lite.Score != 90 || BandName(lite.Band) != "clean" {
		t.Fatalf("assessment = %+v (ok=%v)", lite, ok)
	}
	// The projection is keyed by the un-mapped address.
	if _, ok := snap.Assessment(netip.MustParseAddr("::ffff:203.0.113.5")); !ok {
		t.Fatal("v4-mapped addresses must map to the same entry")
	}

	snap.SetCheck("n1", "netflix", OutcomeLite{Outcome: OutcomeBlocked, ValidUntil: 10, ObservedAt: 1})
	outcomes := snap.NodeCheckOutcomes("n1")
	if len(outcomes) != 1 || outcomes["netflix"].Outcome != OutcomeBlocked {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if _, ok := snap.CheckOutcome("n2", "netflix"); ok {
		t.Fatal("unknown node must not report an outcome")
	}
	snap.DeleteNodeChecks("n1")
	if _, ok := snap.CheckOutcome("n1", "netflix"); ok {
		t.Fatal("checks must be gone after DeleteNodeChecks")
	}

	snap.Clear()
	if a, c := snap.Len(); a != 0 || c != 0 {
		t.Fatalf("Len after Clear = %d/%d", a, c)
	}
}

func TestSnapshot_LoadFromIsAtomicOnError(t *testing.T) {
	snap := NewSnapshot()
	ip := netip.MustParseAddr("198.51.100.99")
	snap.SetAssessment(ip, AssessmentLite{Score: 50})

	if err := snap.LoadFrom(context.Background(), failingSource{}); err == nil {
		t.Fatal("expected the load error to surface")
	}
	if _, ok := snap.Assessment(ip); !ok {
		t.Fatal("a failed reload must keep the previous projection")
	}
	// A nil source is a no-op, never a panic.
	if err := snap.LoadFrom(context.Background(), nil); err != nil {
		t.Fatalf("LoadFrom(nil) = %v", err)
	}
}

type failingSource struct{}

func (failingSource) ListAllAssessments(context.Context) ([]store.Assessment, error) {
	return nil, errTestLoad
}

func (failingSource) ListAllNodeChecks(context.Context) ([]store.NodeCheck, error) {
	return nil, nil
}

var errTestLoad = errorString("load failed")

type errorString string

func (e errorString) Error() string { return string(e) }
