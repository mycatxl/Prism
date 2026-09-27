package intel

import "testing"

// TestParseAnsweredViaNodeSources pins the guard that stops a parked step 4 from
// re-asking a data source it already has an answer from. The summary a previous
// attempt stored is the only record of that, so the parser has to be exact: a
// miss re-spends the node's quota on the same address (measured on 2026-10-02,
// per-node used reached 31-70 for a step that needs one call per source), and a
// false positive would leave a data source unasked forever.
func TestParseAnsweredViaNodeSources(t *testing.T) {
	t.Run("reads the stored list", func(t *testing.T) {
		got := parseAnsweredViaNodeSources(`{"via_node_answered":["ippure","ip_api"],"via_node_lookups":2}`)
		if len(got) != 2 || !got["ippure"] || !got["ip_api"] {
			t.Fatalf("answered = %v, want exactly ippure and ip_api", got)
		}
	})

	t.Run("ignores the other summary keys", func(t *testing.T) {
		// runItem merges every step's summary into one map, and DeferJobItem
		// stores that whole map, so step 4's key has to be found among the rest.
		got := parseAnsweredViaNodeSources(
			`{"egress_ip":"1.2.3.4","via_node_answered":["proxycheck_node"],"skipped":"x","via_node_failed":0}`)
		if len(got) != 1 || !got["proxycheck_node"] {
			t.Fatalf("answered = %v, want just proxycheck_node", got)
		}
	})

	t.Run("no list means no answers", func(t *testing.T) {
		for _, in := range []string{"", "{}", `{"via_node_answered":[]}`, `{"via_node_lookups":3}`, "null"} {
			if got := parseAnsweredViaNodeSources(in); len(got) != 0 {
				t.Fatalf("parseAnsweredViaNodeSources(%q) = %v, want nothing", in, got)
			}
		}
	})

	t.Run("broken json is not a crash and not an answer", func(t *testing.T) {
		// A truncated or mistyped summary must not be read as "everything was
		// already asked" - that would silently skip every data source.
		for _, in := range []string{"{", `{"via_node_answered":"nope"}`, `{"via_node_answered":{"a":1}}`} {
			if got := parseAnsweredViaNodeSources(in); len(got) != 0 {
				t.Fatalf("parseAnsweredViaNodeSources(%q) = %v, want nothing", in, got)
			}
		}
	})

	t.Run("the older via_node_sources key is not an answer", func(t *testing.T) {
		// A summary written by the first version of this guard lists every source
		// it asked, failures included. Reading that as "answered" would skip the
		// failed ones for the rest of the item's life, so the parser deliberately
		// ignores it - which also means an item parked by the previous build
		// simply re-asks everything once.
		got := parseAnsweredViaNodeSources(`{"via_node_sources":["ippure","ip_api","proxycheck_node"]}`)
		if len(got) != 0 {
			t.Fatalf("answered = %v, want nothing from the legacy key", got)
		}
	})

	t.Run("with both keys only the answered one counts", func(t *testing.T) {
		got := parseAnsweredViaNodeSources(
			`{"via_node_sources":["ippure","ip_api"],"via_node_answered":["ippure"]}`)
		if len(got) != 1 || !got["ippure"] {
			t.Fatalf("answered = %v, want just ippure", got)
		}
	})
}
