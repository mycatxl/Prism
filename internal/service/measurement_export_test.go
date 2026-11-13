package service

import (
	"context"
	"encoding/json"
	"prism/internal/export"
	"prism/internal/intel/store"
	"prism/internal/quality"
	"testing"
	"time"
)

func TestCanonicalExportUsesMeasuredFactsAndTriStateSignals(t *testing.T) {
	f := buildIntelNodeFixture(t)
	ctx := context.Background()
	e, _ := f.cp.Pool.GetEntry(f.clean)
	ip := e.GetEgressIP().String()
	if err := f.svc.Store().UpsertNodeEgress(ctx, store.NodeEgress{NodeHash: f.clean.Hex(), IPv4: ip, IPv6: "2001:db8::10", Colo: "NRT", Loc: "JP"}); err != nil {
		t.Fatal(err)
	}
	no := false
	ev := quality.Evidence{IP: ip, Provider: "proxycheck_node", Signals: quality.Signals{Proxy: &no}, ObservedAt: time.Now(), ValidUntil: time.Now().Add(time.Hour)}
	encoded, _ := json.Marshal(ev)
	if err := f.svc.Store().UpsertEvidence(ctx, store.Evidence{IP: ip, Provider: "proxycheck_node", Status: store.StatusOk, ObservedAtNs: ev.ObservedAt.UnixNano(), NormalizedJSON: string(encoded), ValidUntilNs: ev.ValidUntil.UnixNano()}); err != nil {
		t.Fatal(err)
	}
	risk := 5
	pure := quality.Evidence{IP: ip, Provider: "ippure", FraudScore: &risk, ObservedAt: ev.ObservedAt, ValidUntil: ev.ValidUntil}
	pureJSON, _ := json.Marshal(pure)
	if err := f.svc.Store().UpsertEvidence(ctx, store.Evidence{IP: ip, Provider: "ippure", Status: store.StatusOk, ObservedAtNs: pure.ObservedAt.UnixNano(), ValidUntilNs: pure.ValidUntil.UnixNano(), NormalizedJSON: string(pureJSON)}); err != nil {
		t.Fatal(err)
	}
	summary := f.cp.nodeEntryToSummary(f.clean, e)
	item, ok := f.cp.ExportItem(summary)
	if !ok {
		t.Fatal("export missing")
	}
	body, _, _, err := export.Export([]export.Item{item}, export.FormatJSON, export.Options{IncludeIntel: true})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Nodes []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Nodes) != 1 {
		t.Fatalf("unexpected export %s", body)
	}
	row := out.Nodes[0]
	for k, want := range map[string]string{"egress_ipv4": ip, "egress_ipv6": "2001:db8::10", "colo": "NRT", "proxy": "false", "vpn": "", "tor": ""} {
		if row[k] != want {
			t.Fatalf("%s=%v want %s", k, row[k], want)
		}
	}
	if row["purity_score"] == nil || row["assessed_at"] == "" {
		t.Fatal("real assessment lost", row)
	}
}
