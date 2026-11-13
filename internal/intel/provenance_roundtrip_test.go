package intel

import (
	"context"
	"encoding/json"
	"net/netip"
	"prism/internal/intel/assess"
	"prism/internal/intel/store"
	"prism/internal/quality"
	"strings"
	"testing"
	"time"
)

func TestPurityFieldProvenanceSurvivesStoreRoundtrip(t *testing.T) {
	enabled := false
	svc := newAutoEnqueueService(t, &enabled)
	defer svc.Stop()
	now := time.Now().UTC()
	ip := netip.MustParseAddr("203.0.113.21")
	risk := 20
	yes := true
	var evidence []quality.Evidence
	for _, p := range []string{"ippure", "proxycheck", "ipapi_is", "ip_api", "ipqs", "dbip_lite", "maxmind_geolite2", "ipinfo_lite", "geo_country"} {
		evidence = append(evidence, quality.Evidence{IP: ip.String(), Provider: p, FraudScore: &risk, IsResidential: &yes, IsMobile: &yes, IPType: "residential", ASNNumber: 64500, Organization: strings.Repeat("Org", 60), CountryCode: "US", City: strings.Repeat("City", 40), Region: strings.Repeat("Region", 25), ObservedAt: now, ValidUntil: now.Add(time.Hour)})
	}
	result := assess.Assess(ip, evidence, now, nil)
	row := AssessmentRow(result, now)
	if len(row.ComponentsJSON) <= store.MaxNormalizedJSONBytes {
		t.Fatal("fixture must exceed old single-evidence limit")
	}
	if err := svc.Store().UpsertAssessment(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	got, ok, err := svc.Store().GetAssessment(context.Background(), ip.String())
	if err != nil || !ok {
		t.Fatal(err)
	}
	var blob componentsBlob
	if err := json.Unmarshal([]byte(got.ComponentsJSON), &blob); err != nil {
		t.Fatal(err)
	}
	if len(blob.Facts["country"].Observations) != 9 || blob.Facts["purity"].Provider != "ippure" {
		t.Fatal("field provenance silently truncated")
	}
}
