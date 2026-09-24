package intel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"

	"prism/internal/intel/checks"
	"prism/internal/intel/egress"
	"prism/internal/intel/jobs"
	"prism/internal/intel/providers"
	"prism/internal/intel/store"
	"prism/internal/model"
	"prism/internal/netutil"
	"prism/internal/node"
)

// e2eEgressIP is the address the fake trace endpoint reports for the node.
const e2eEgressIP = "8.8.8.8"

// e2eCheckID is the unlock rule the end-to-end job runs.
const e2eCheckID = "prism_e2e"

// PRISM-DEVIATION: none. This test replaces the former in-memory inspection
// path (WP08 §8), it does not adjust an upstream assertion.

// loopbackOutbound is the in-process "node under test": every dial lands on the
// local test server, so no test ever touches the network.
type loopbackOutbound struct{ target string }

func (o *loopbackOutbound) Type() string           { return "direct" }
func (o *loopbackOutbound) Tag() string            { return "prism-e2e-node" }
func (o *loopbackOutbound) Network() []string      { return []string{"tcp"} }
func (o *loopbackOutbound) Dependencies() []string { return nil }
func (o *loopbackOutbound) Close() error           { return nil }
func (o *loopbackOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("loopback outbound: packet connections are not supported")
}

func (o *loopbackOutbound) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", o.target)
}

// nodeFetcher performs the egress trace request through the node outbound,
// exactly like the production probe fetcher of cmd/prism.
type nodeFetcher struct{ outbound adapter.Outbound }

func (f nodeFetcher) FetchWithOptions(ctx context.Context, _ node.Hash, url string, opts netutil.OutboundHTTPOptions) ([]byte, time.Duration, error) {
	return netutil.HTTPGetViaOutbound(ctx, f.outbound, url, opts)
}

// fakeProviderServer answers the trace endpoint, every data source endpoint the
// pipeline calls and the unlock check target.
func fakeProviderServer(t *testing.T) *httptest.Server {
	t.Helper()
	const proxyCheck = `{"status":"ok","8.8.8.8":{"risk":12,` +
		`"network":{"asn":"AS15169","organisation":"Google LLC","provider":"Google","type":"business"},` +
		`"location":{"country_code":"US","city":"Mountain View","region":"California"},` +
		`"detections":{"proxy":false,"vpn":false,"tor":false,"hosting":false,"compromised":false,` +
		`"scraper":false,"anonymous":false,"risk":12,"confidence":97},` +
		`"operator":{"name":"Google Fiber","services":["isp","hosting"]},` +
		`"attack_history":{"http":3,"smtp":0},"last_updated":"2020-01-01T00:00:00Z"}}`
	const ipAPI = `{"status":"success","country":"United States","countryCode":"US",` +
		`"regionName":"California","city":"Mountain View","isp":"Google LLC","org":"Google LLC",` +
		`"as":"AS15169 Google LLC","asname":"GOOGLE","mobile":false,"proxy":false,` +
		`"hosting":true,"query":"` + e2eEgressIP + `"}`
	const ipPure = `{"ip":"` + e2eEgressIP + `","asn":15169,"asOrganization":"Google LLC",` +
		`"countryCode":"US","fraudScore":10,"isResidential":false,"isBroadcast":false}`

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/trace/v4"):
			io.WriteString(w, "fl=1f2\nh=1.1.1.1\nip="+e2eEgressIP+"\nloc=JP\ncolo=NRT\n")
		case strings.HasPrefix(r.URL.Path, "/trace/v6"):
			// No ip= line: the node has no usable IPv6 egress (§3.2).
			io.WriteString(w, "fl=1f2\nh=1.1.1.1\n")
		case strings.HasPrefix(r.URL.Path, "/proxycheck/"):
			io.WriteString(w, proxyCheck)
		case strings.HasPrefix(r.URL.Path, "/ip-api"):
			io.WriteString(w, ipAPI)
		case strings.HasPrefix(r.URL.Path, "/ippure"):
			io.WriteString(w, ipPure)
		case strings.HasPrefix(r.URL.Path, "/check/home"):
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<html>prism e2e</html>")
		default:
			http.NotFound(w, r)
		}
	}))
}

// e2eRegistry builds the real provider registry against the test server: the
// offline sources stay local, every network source points at httptest.
func e2eRegistry(t *testing.T, serverURL string) *providers.Registry {
	t.Helper()
	registry := providers.NewRegistry()
	providers.RegisterBuiltins(registry, providers.BuiltinConfig{
		GeoDir:  t.TempDir(),
		Country: countryLookup("JP"),
		Client:  providers.NewStrictClient(5 * time.Second),
		Timeout: 5 * time.Second,
		Now:     time.Now,
	})

	rows := []model.IntelProviderSetting{
		{ProviderID: "geo_country", Enabled: true},
		// Spamhaus et al. must never be queried from a test (and not from CI).
		{ProviderID: "dnsbl", Enabled: false},
		// Onionoo would schedule a background refresh; the offline Tor source is
		// covered by its own unit tests.
		{ProviderID: "torproject", Enabled: false},
		{ProviderID: "dbip_lite", Enabled: true},
		{ProviderID: "proxycheck", Enabled: true, DailyLimit: 100, QPS: 5,
			ConfigJSON: `{"url":"` + serverURL + `/proxycheck/"}`},
		{ProviderID: "ip_api", Enabled: true,
			ConfigJSON: `{"url":"` + serverURL + `/ip-api"}`},
		{ProviderID: "ippure", Enabled: true, DailyLimit: 100, QPS: 5,
			ConfigJSON: `{"url":"` + serverURL + `/ippure"}`},
	}
	registry.Apply(providers.ResolveSettings(registry.Specs(), rows, nil))
	return registry
}

// countryLookup is a fixed offline country database for tests.
type countryLookup string

func (c countryLookup) Lookup(netip.Addr) string { return string(c) }

// writeUserCheckRule installs one user rule pointing at the test server.
func writeUserCheckRule(t *testing.T, dir, target string) {
	t.Helper()
	ruleDir := filepath.Join(dir, "checks.d")
	if err := os.MkdirAll(ruleDir, 0o755); err != nil {
		t.Fatalf("create check rule dir: %v", err)
	}
	rule := `id: ` + e2eCheckID + `
version: 1
name: Prism E2E check
category: other
enabled: true
ttl: 1h
timeout: 5s
calibrated: 2026-01-01
steps:
  - id: home
    request:
      method: GET
      url: ` + target + `
      follow_redirects: false
outcomes:
  - when: {step: home, status_in: [200]}
    outcome: available
  - when: {step: home, status_not_in: [200]}
    outcome: blocked
default: unknown
`
	if err := os.WriteFile(filepath.Join(ruleDir, e2eCheckID+".yaml"), []byte(rule), 0o600); err != nil {
		t.Fatalf("write check rule: %v", err)
	}
}

// openIntelStore opens a throw-away intel.db.
func openIntelStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatalf("open intel store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestIntelPipelineEndToEnd proves the whole WP09 chain without the network:
// egress probe -> offline sources -> online queue -> via-node sources -> unlock
// checks -> evidence-gated assessment, all persisted in intel.db and mirrored
// into the in-memory projection.
func TestIntelPipelineEndToEnd(t *testing.T) {
	st := openIntelStore(t)
	ctx := context.Background()

	server := fakeProviderServer(t)
	defer server.Close()
	serverAddr := strings.TrimPrefix(server.URL, "http://")
	nodeOutbound := &loopbackOutbound{target: serverAddr}

	hash := node.HashFromRawOptions([]byte(`{"type":"direct","tag":"prism-e2e"}`))
	nodeHash := hash.String()

	stateDir := t.TempDir()
	writeUserCheckRule(t, stateDir, server.URL+"/check/home")
	checkEngine := checks.NewEngine(checks.Options{UserDir: filepath.Join(stateDir, "checks.d")})
	if errs := checkEngine.LoadErrors(); len(errs) > 0 {
		t.Fatalf("check rule load errors: %v", errs)
	}
	if _, ok := checkEngine.Rule(e2eCheckID); !ok {
		t.Fatalf("check rule %s was not loaded", e2eCheckID)
	}

	registry := e2eRegistry(t, server.URL)
	prober := &egress.Probe{
		Fetcher: nodeFetcher{outbound: nodeOutbound},
		Store:   st,
		URLv4:   server.URL + "/trace/v4",
		URLv6:   server.URL + "/trace/v6",
		Timeout: 5 * time.Second,
	}
	stepOptions := StepOptions{
		Store:    st,
		Registry: registry,
		Checks:   checkEngine,
		Outbound: func(string) (adapter.Outbound, bool) { return nodeOutbound, true },
		Config: func() jobs.Config {
			return jobs.Config{Enabled: true, NodeWorkers: 2, MaxRunningJobs: 1}.Normalize()
		},
		Clock: jobs.SystemClock{},
	}
	svc := newE2EService(t, st, prober, stepOptions, registry)
	if err := svc.Start(); err != nil {
		t.Fatalf("start intel service: %v", err)
	}
	defer svc.Stop()

	job, err := svc.CreateJob(ctx, jobs.Request{
		Kind:   jobs.KindFull,
		Scope:  jobs.Scope{NodeHashes: []string{nodeHash}},
		Checks: []string{e2eCheckID},
	}, jobs.CreatedByAdmin(), jobs.PriorityManual)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	// --- step 1: the egress observation lands in intel.db ------------------
	waitFor(t, 10*time.Second, "node egress row", func() bool {
		row, ok, err := st.GetNodeEgress(ctx, nodeHash)
		return err == nil && ok && row.IPv4 == e2eEgressIP
	})
	egressRow, ok, err := st.GetNodeEgress(ctx, nodeHash)
	if err != nil || !ok {
		t.Fatalf("read node egress: ok=%v err=%v", ok, err)
	}
	if egressRow.IPv4 != e2eEgressIP || egressRow.Colo != "NRT" || egressRow.Loc != "JP" {
		t.Fatalf("unexpected egress row: %+v", egressRow)
	}
	if egressRow.IPv6 != "" {
		t.Fatalf("the node must have no IPv6 egress, got %q", egressRow.IPv6)
	}
	history, err := st.ListEgressHistory(ctx, nodeHash, 10)
	if err != nil || len(history) != 1 {
		t.Fatalf("egress history: len=%d err=%v", len(history), err)
	}

	// --- steps 2/4: offline and via-node evidence ---------------------------
	waitFor(t, 15*time.Second, "offline and via-node evidence", func() bool {
		_, geoOK, _ := st.GetEvidence(ctx, e2eEgressIP, "geo_country")
		_, apiOK, _ := st.GetEvidence(ctx, e2eEgressIP, "ip_api")
		_, pureOK, _ := st.GetEvidence(ctx, e2eEgressIP, "ippure")
		return geoOK && apiOK && pureOK
	})
	assertEvidence(t, st, "geo_country", store.StatusOk, "")
	assertEvidence(t, st, "dbip_lite", store.StatusError, providers.CodeUnavailable)
	assertEvidence(t, st, "ip_api", store.StatusOk, "")
	assertEvidence(t, st, "ippure", store.StatusOk, "")
	for _, id := range []string{"ip_api", "ippure"} {
		row, ok, err := st.GetEvidence(ctx, e2eEgressIP, id)
		if err != nil || !ok {
			t.Fatalf("read %s evidence: ok=%v err=%v", id, ok, err)
		}
		if row.ViaNodeHash != nodeHash {
			t.Fatalf("%s evidence must record the node it ran through, got %q", id, row.ViaNodeHash)
		}
	}

	// --- step 3/§3.3: the online queue resolved proxycheck ------------------
	waitFor(t, 15*time.Second, "online provider queue", func() bool {
		return providerRowResolved(t, st, "proxycheck")
	})
	assertEvidence(t, st, "proxycheck", store.StatusOk, "")

	// --- step 5: the unlock check result ------------------------------------
	waitFor(t, 15*time.Second, "node check row", func() bool {
		row, ok, err := st.GetNodeCheck(ctx, nodeHash, e2eCheckID)
		return err == nil && ok && row.Outcome == checks.OutcomeAvailable
	})
	checkRow, _, err := st.GetNodeCheck(ctx, nodeHash, e2eCheckID)
	if err != nil {
		t.Fatalf("read node check: %v", err)
	}
	if checkRow.EgressIP != e2eEgressIP {
		t.Fatalf("check egress ip = %q, want %q", checkRow.EgressIP, e2eEgressIP)
	}

	// --- step 6: the assessment and the job result ---------------------------
	job = waitForTerminalJob(t, st, job.ID)
	if job.Status != store.JobSucceeded {
		t.Fatalf("job status = %q, want %q", job.Status, store.JobSucceeded)
	}

	ip := netip.MustParseAddr(e2eEgressIP)
	assessment, found, err := st.GetAssessment(ctx, e2eEgressIP)
	if err != nil || !found {
		t.Fatalf("ip_assessment row missing: found=%v err=%v", found, err)
	}
	if assessment.Profile != "prism-purity-v2" || assessment.Verdict == "" || assessment.Confidence == "" {
		t.Fatalf("unexpected assessment: %+v", assessment)
	}
	if !assessment.PurityScore.Valid || !assessment.Native.Valid {
		t.Fatalf("assessment must carry a score and a native verdict: %+v", assessment)
	}

	// The projection mirrors both the assessment and the check outcome (§5).
	lite, ok := svc.Snapshot().Assessment(ip)
	if !ok {
		t.Fatalf("the in-memory projection has no assessment for %s", e2eEgressIP)
	}
	if lite.Band != BandCode(assessment.PurityBand) {
		t.Fatalf("projection band %v does not match the stored band %q", lite.Band, assessment.PurityBand)
	}
	if _, ok := svc.Snapshot().CheckOutcome(nodeHash, e2eCheckID); !ok {
		t.Fatalf("the in-memory projection has no %s outcome for %s", e2eCheckID, nodeHash)
	}

	// The step summaries explain what happened for the operator.
	item, ok, err := st.GetJobItem(ctx, job.ID, nodeHash)
	if err != nil || !ok {
		t.Fatalf("read job item: ok=%v err=%v", ok, err)
	}
	if item.Status != store.ItemDone {
		t.Fatalf("job item status = %q error=%q", item.Status, item.ErrorCode)
	}
	for _, key := range []string{"offline_lookups", "online_enqueued", "via_node_lookups", "checks", "assessed"} {
		if !strings.Contains(item.ResultJSON, `"`+key+`"`) {
			t.Fatalf("job item result is missing %q: %s", key, item.ResultJSON)
		}
	}

	// Re-running the pipeline refreshes only what is missing (§3.2 step 3):
	// dropping the proxycheck evidence must re-queue the terminal queue row and
	// produce fresh evidence, while the still-valid sources are not re-queued.
	if err := st.DeleteEvidence(ctx, e2eEgressIP, "proxycheck"); err != nil {
		t.Fatalf("delete proxycheck evidence: %v", err)
	}
	refresh, err := svc.CreateJob(ctx, jobs.Request{
		Kind:   jobs.KindIntel,
		Scope:  jobs.Scope{NodeHashes: []string{nodeHash}},
		Checks: []string{e2eCheckID},
	}, jobs.CreatedByRefresh(), jobs.PriorityRefresh)
	if err != nil {
		t.Fatalf("create refresh job: %v", err)
	}
	waitFor(t, 15*time.Second, "refreshed online evidence", func() bool {
		return providerRowResolved(t, st, "proxycheck") && evidencePresent(t, st, "proxycheck")
	})
	if refreshed := waitForTerminalJob(t, st, refresh.ID); refreshed.Status != store.JobSucceeded {
		t.Fatalf("refresh job status = %q, want %q", refreshed.Status, store.JobSucceeded)
	}
}

// evidencePresent reports whether a usable evidence row exists for the test IP.
func evidencePresent(t *testing.T, st *store.Store, provider string) bool {
	t.Helper()
	row, ok, err := st.GetEvidence(context.Background(), e2eEgressIP, provider)
	if err != nil {
		t.Fatalf("read %s evidence: %v", provider, err)
	}
	return ok && row.Status == store.StatusOk
}

// assertEvidence checks one stored evidence row.
func assertEvidence(t *testing.T, st *store.Store, provider, status, errorCode string) {
	t.Helper()
	row, ok, err := st.GetEvidence(context.Background(), e2eEgressIP, provider)
	if err != nil || !ok {
		t.Fatalf("evidence %s missing: ok=%v err=%v", provider, ok, err)
	}
	if row.Status != status {
		t.Fatalf("evidence %s status = %q, want %q (%q)", provider, row.Status, status, row.ErrorCode)
	}
	if errorCode != "" && row.ErrorCode != errorCode {
		t.Fatalf("evidence %s error = %q, want %q", provider, row.ErrorCode, errorCode)
	}
}

// providerRowResolved reports whether the queue row of one provider is done.
func providerRowResolved(t *testing.T, st *store.Store, provider string) bool {
	t.Helper()
	counts, err := st.ProviderQueueCounts(context.Background(), provider)
	if err != nil {
		t.Fatalf("provider queue counts: %v", err)
	}
	return counts.Done > 0
}

// TestAssessStepWaitsForEvidence proves the assessment step never writes a
// bogus verdict: without an egress observation or without evidence it reports a
// named reason and leaves ip_assessment untouched.
func TestAssessStepWaitsForEvidence(t *testing.T) {
	st := openIntelStore(t)
	ctx := context.Background()

	hash := node.HashFromRawOptions([]byte(`{"type":"direct","tag":"prism-assess"}`))
	nodeHash := hash.String()
	ip := netip.MustParseAddr(e2eEgressIP)

	svc, err := NewService(Options{
		Store: st,
		Scope: jobs.ScopeResolverFunc(func(context.Context, jobs.Scope) ([]string, error) {
			return []string{nodeHash}, nil
		}),
		Config: func() jobs.Config { return jobs.Config{Enabled: true}.Normalize() },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	// Step 6 before step 1: no egress observation exists yet.
	result := svc.assessStep(ctx, "job-out-of-order", nodeHash)
	if _, ok := result.Summary["skipped"]; !ok {
		t.Fatalf("assessment without egress must report a skip: %v", result.Summary)
	}
	if result.ErrorCode != "" {
		t.Fatalf("assessment without egress must not fail the item: %q", result.ErrorCode)
	}
	if _, found, _ := st.GetAssessment(ctx, e2eEgressIP); found {
		t.Fatal("assessment without an egress observation must not persist a verdict")
	}

	// Step 6 after step 1 but before the evidence steps.
	if err := st.UpsertNodeEgress(ctx, store.NodeEgress{NodeHash: nodeHash, IPv4: e2eEgressIP}); err != nil {
		t.Fatalf("seed node egress: %v", err)
	}
	result = svc.assessStep(ctx, "job-out-of-order", nodeHash)
	reason, ok := result.Summary["skipped"].(string)
	if !ok || !strings.Contains(reason, e2eEgressIP) {
		t.Fatalf("assessment without evidence must name the address: %v", result.Summary)
	}
	if _, found, _ := st.GetAssessment(ctx, e2eEgressIP); found {
		t.Fatal("assessment without evidence must not persist a verdict")
	}

	// Now the evidence arrives: the step assesses and persists.
	row := store.Evidence{
		IP: e2eEgressIP, Provider: "proxycheck", Profile: providers.ProxyCheckProfile,
		Status: store.StatusOk, ObservedAtNs: time.Now().UnixNano(),
		ValidUntilNs:   time.Now().Add(time.Hour).UnixNano(),
		NormalizedJSON: `{"ip":"` + e2eEgressIP + `","provider":"proxycheck","ip_type":"business","risk_score":12}`,
	}
	if err := st.UpsertEvidence(ctx, row); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}
	result = svc.assessStep(ctx, "job-in-order", nodeHash)
	if result.ErrorCode != "" {
		t.Fatalf("assessment failed: %q %v", result.ErrorCode, result.Summary)
	}
	if count, ok := result.Summary["assessed"].(int); !ok || count != 1 {
		t.Fatalf("assessment summary = %v, want assessed=1", result.Summary)
	}
	assessment, found, err := st.GetAssessment(ctx, e2eEgressIP)
	if err != nil || !found {
		t.Fatalf("assessment row missing: found=%v err=%v", found, err)
	}
	if assessment.State == "" {
		t.Fatalf("assessment without state: %+v", assessment)
	}
	if _, ok := svc.Snapshot().Assessment(ip); !ok {
		t.Fatal("the assessment projection was not updated")
	}
}

// newE2EService assembles the service under test with a fast scheduler tick.
func newE2EService(t *testing.T, st *store.Store, prober *egress.Probe, steps StepOptions, registry *providers.Registry) *Service {
	t.Helper()
	svc, err := NewService(Options{
		Store: st,
		Scope: jobs.ScopeResolverFunc(func(_ context.Context, scope jobs.Scope) ([]string, error) {
			return scope.NodeHashes, nil
		}),
		Prober:        prober,
		Handlers:      NewStepHandlers(steps),
		Config:        steps.Config,
		ProviderSpecs: QueueSpecs(registry),
		Lookup:        NewOnlineLookup(steps),
		EnabledSources: func() map[string]bool {
			return map[string]bool{"geo_country": true, "dbip_lite": true, "proxycheck": true}
		},
		Census: nil,
		Logf:   t.Logf,
		Tick:   20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// waitFor polls cond until it holds or the deadline expires.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// waitForTerminalJob waits until a job reaches a terminal status.
func waitForTerminalJob(t *testing.T, st *store.Store, jobID string) store.Job {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		job, err := st.GetJob(ctx, jobID)
		if err != nil {
			t.Fatalf("read job %s: %v", jobID, err)
		}
		if job.Terminal() {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach a terminal state", jobID)
	return store.Job{}
}
