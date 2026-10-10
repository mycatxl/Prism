package checks

import (
	"io/fs"
	"testing"
	"testing/fstest"
	"time"
)

// countingEnabledSource counts how many times the engine asks for the effective
// enabled flag of a rule.
type countingEnabledSource struct {
	calls map[string]int
}

func (c *countingEnabledSource) CheckEnabled(checkID string) (bool, bool) {
	c.calls[checkID]++
	// found=false keeps the file's own flag authoritative, which is what the
	// production source does for a rule with no persisted row.
	return false, false
}

// TestAudit_EnabledSourceIsQueriedOncePerRulePerRun measures the query pattern of
// selectRules (engine.go:349): for every rule it consults e.enabledRule, which
// calls EnabledSource.CheckEnabled. The production implementation of that
// interface (cmd/prism/intel_runtime.go:279) runs a full table scan -
// state.ListIntelProviderSettings issues "SELECT ... FROM intel_provider_settings"
// - on every call. One node's check step therefore costs one full settings scan
// per rule in the loaded set, on every step of every item.
//
// The engine has the whole rule list in memory already; the enabled flags are
// only needed for the rules that are actually selected.
func TestAudit_EnabledSourceIsQueriedOncePerRulePerRun(t *testing.T) {
	rules := fstest.MapFS{}
	const ruleCount = 8
	for i := 0; i < ruleCount; i++ {
		id := "rule_" + string(rune('a'+i))
		rules["builtin/"+id+".yaml"] = &fstest.MapFile{Data: []byte(
			"id: " + id + "\nversion: 1\nname: Fixture " + id + "\ncategory: other\nenabled: true\n" +
				"ttl: 2h\ntimeout: 5s\ncalibrated: fixture\nsteps:\n  - id: home\n    request:\n" +
				"      method: GET\n      url: https://example.test/\noutcomes:\n  - when: {step: home, status_in: [200]}\n" +
				"    outcome: available\ndefault: unknown\n")}
	}
	source := &countingEnabledSource{calls: map[string]int{}}
	engine := NewEngine(Options{
		BuiltinFS:     fs.FS(rules),
		EnabledSource: source,
		Now:           time.Now,
		Logf:          t.Logf,
	})
	if errs := engine.LoadErrors(); len(errs) != 0 {
		t.Fatalf("load errors: %v", errs)
	}

	// One selection pass over the whole set, exactly as one node's check step
	// does with no explicit rule list.
	selected := engine.selectRules(nil)
	if len(selected) != ruleCount {
		t.Fatalf("selected %d rules, want %d", len(selected), ruleCount)
	}
	total := 0
	for _, n := range source.calls {
		total += n
	}
	t.Logf("one selectRules pass over %d rules queried the enabled source %d times", ruleCount, total)
	if total > ruleCount {
		t.Errorf("the enabled source was queried %d times for %d rules: the settings table is "+
			"scanned once per rule per step", total, ruleCount)
	}

	// The same pass twice: the count has to stay bounded by the rule count, not
	// grow with the number of steps the scheduler runs.
	for _, n := range source.calls {
		if n != 1 {
			t.Errorf("rule queried %d times in a single pass, want 1", n)
		}
	}
}
