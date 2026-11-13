package service

import (
	"context"
	"net/netip"
	"sort"
	"strings"
	"time"

	"prism/internal/intel"
	"prism/internal/intel/assess"
	"prism/internal/intel/jobs"
	"prism/internal/intel/providers"
	"prism/internal/intel/store"
	"prism/internal/node"
	"prism/internal/quality"
)

// The legacy /api/v1/quality/* surface, reimplemented over the intel subsystem
//
//
// The old internal/inspection in-memory manager was replaced by intel.db, so
// these handlers are now aliases: they read the authoritative intel rows and map
// them onto the quality.Summary shape the frontend already renders. The response
// shape is unchanged — internal/api/web/src/features/quality/types.ts is the
// contract, and the WebUI was not rewritten.
//
// The handlers deliberately keep returning the legacy status codes so the
// existing frontend error handling keeps working: a disabled intel subsystem is
// a 409 CONFLICT, exactly as "quality inspection is disabled" used to be.

// Assessment state and verdict names of the intel store. They are repeated here
// as private constants so this projection cannot silently follow a rename in
// another package without the alias surface being revisited.
const (
	assessmentVerdictFavorable = "favorable"
	assessmentVerdictHighRisk  = "high_risk"
	assessmentVerdictConflict  = "conflicting"

	assessmentIPTypeConflict = "conflicting"
)

// qualityStatusResponse is the GET /api/v1/quality/status body.
//
// It is built from intel data, but the field names are the ones
// internal/api/web/src/features/quality/types.ts declares. Every list is always
// an array, never null, and storage_error is always a string: the WebUI renders
// the disabled shape too.
type qualityStatusResponse struct {
	Enabled             bool                          `json:"enabled"`
	KnownIPs            int                           `json:"known_ips"`
	CheckedIPs          int                           `json:"checked_ips"`
	LowRiskIPs          int                           `json:"low_risk_ips"`
	HighRiskIPs         int                           `json:"high_risk_ips"`
	StaleIPs            int                           `json:"stale_ips"`
	QueueCapacity       int                           `json:"queue_capacity"`
	DroppedObservations uint64                        `json:"dropped_observations"`
	StorageError        string                        `json:"storage_error"`
	Sources             []qualitySourceStatus         `json:"sources"`
	ManualSources       []qualityManualSourceStatus   `json:"manual_sources"`
	RegistrySources     []qualityRegistrySourceStatus `json:"registry_sources"`
}

// qualitySourceStatus is one entry of the status source list: the providers
// queried from this host.
type qualitySourceStatus struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Website      string `json:"website"`
	Configured   bool   `json:"configured"`
	RequiresKey  bool   `json:"requires_key"`
	HasKey       bool   `json:"has_key"`
	DailyLimit   int    `json:"daily_limit"`
	UsedToday    int    `json:"used_today"`
	Queued       int    `json:"queued"`
	Running      int    `json:"running"`
	Failed       int    `json:"failed"`
	Paused       bool   `json:"paused"`
	NextAllowed  string `json:"next_allowed_at,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	Terms        string `json:"terms,omitempty"`
	TTLSeconds   int64  `json:"ttl_seconds,omitempty"`
	SupportsIPv6 bool   `json:"supports_ipv6,omitempty"`
}

// qualityManualSourceStatus is one entry of the manual (via-node) source list:
// the providers queried through the node under test.
//
// The field order matches the legacy inspection.ManualSourceStatus the WebUI
// contract test pinned, so the rendered key order is unchanged.
type qualityManualSourceStatus struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Website         string `json:"website"`
	Busy            bool   `json:"busy"`
	IntervalSeconds int    `json:"interval_seconds"`
	NextAllowed     string `json:"next_allowed_at,omitempty"`
	CurrentIPs      int    `json:"current_ips"`
}

// qualityRegistrySourceStatus is one entry of the registry (offline database)
// source list: bundled or downloaded data that never touches the network per
// lookup.
type qualityRegistrySourceStatus struct {
	ID        string `json:"id"`
	Ready     bool   `json:"ready"`
	Entries   int    `json:"entries"`
	UpdatedAt string `json:"updated_at,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

// qualityStorageUnavailableCode is the storage_error the frontend already knows
// (see internal/api/web/src/features/quality/presentation.ts). A failing read
// never breaks the status page: the counters stay at their last known value and
// the code explains why.
const qualityStorageUnavailableCode = "QUALITY_STORAGE_UNAVAILABLE"

// QualityStatus assembles GET /api/v1/quality/status from the intel subsystem.
//
// It never fails: when the intel subsystem is not wired into this build, or when
// one of its reads fails, the documented disabled shape (or the partial shape
// with storage_error set) is returned. The WebUI renders both.
func (s *ControlPlaneService) QualityStatus(ctx context.Context) qualityStatusResponse {
	status := qualityStatusResponse{
		Sources:         []qualitySourceStatus{},
		ManualSources:   []qualityManualSourceStatus{},
		RegistrySources: []qualityRegistrySourceStatus{},
	}
	svc, serr := s.intelService()
	if serr != nil {
		return status
	}
	status.Enabled = svc.Manager().Enabled()
	status.DroppedObservations = uint64(svc.DiscardedEgressObservations())
	status.QueueCapacity = svc.Manager().Config().MaxRunningJobs

	st := svc.Store()
	counts, err := st.CountQualitySummary(ctx, time.Now().UTC().UnixNano())
	if err != nil {
		status.StorageError = qualityStorageUnavailableCode
	} else {
		status.KnownIPs = counts.KnownIPs
		status.CheckedIPs = counts.CheckedIPs
		status.LowRiskIPs = counts.LowRiskIPs
		status.HighRiskIPs = counts.HighRiskIPs
		status.StaleIPs = counts.StaleIPs
	}

	// The source table is the provider registry: the same effective settings the
	// pipeline resolves, so the reported budget is the one actually applied.
	usage, err := intelProviderUsage(ctx, st)
	if err != nil {
		status.StorageError = qualityStorageUnavailableCode
		return status
	}
	statuses, err := svc.ProviderSettings().Statuses(usage)
	if err != nil {
		status.StorageError = qualityStorageUnavailableCode
		return status
	}
	for _, provider := range statuses {
		switch {
		case provider.Direct:
			status.Sources = append(status.Sources, qualitySourceStatus{
				ID:           provider.ID,
				Name:         provider.Name,
				Website:      provider.Website,
				Configured:   provider.Enabled,
				RequiresKey:  provider.RequiresKey,
				HasKey:       provider.HasKey,
				DailyLimit:   provider.DailyLimit,
				UsedToday:    provider.Usage.Used,
				Queued:       provider.Usage.Queued,
				Running:      provider.Usage.Running,
				Failed:       provider.Usage.Failed,
				Paused:       provider.Usage.Paused,
				NextAllowed:  qualityTimestamp(provider.Usage.NextAllowedAtNs),
				ErrorCode:    provider.Usage.ErrorCode,
				Terms:        provider.Terms,
				TTLSeconds:   qualitySeconds(provider.TTL),
				SupportsIPv6: provider.SupportsIPv6,
			})
		case provider.ViaNode:
			status.ManualSources = append(status.ManualSources, qualityManualSourceStatus{
				ID:              provider.ID,
				Name:            provider.Name,
				Website:         provider.Website,
				Busy:            provider.Usage.Running > 0 || provider.Usage.Queued > 0,
				IntervalSeconds: int(qualitySeconds(provider.TTL)),
				CurrentIPs:      qualityEvidenceCount(ctx, st, provider.ID),
				NextAllowed:     qualityTimestamp(provider.Usage.NextAllowedAtNs),
			})
		default:
			// Offline sources are registry data: they are bundled or downloaded
			// rather than queried, so they have no budget.
			status.RegistrySources = append(status.RegistrySources,
				qualityRegistrySource(provider.ID, provider.Database))
		}
	}
	return status
}

// qualityRegistrySource projects the download state of one offline provider.
func qualityRegistrySource(id string, database *providers.DatabaseStatus) qualityRegistrySourceStatus {
	entry := qualityRegistrySourceStatus{ID: id}
	if database == nil {
		return entry
	}
	entry.Ready = database.Installed && database.Ready
	entry.ErrorCode = database.ErrorCode
	entry.UpdatedAt = qualityTimestamp(database.LastSuccessAtNs)
	for _, file := range database.Files {
		if file.Installed {
			entry.Entries++
		}
	}
	return entry
}

// qualityEvidenceCount reports how many IPs have usable evidence of one
// provider. It is the "已复核" counter of the manual source cards; a read
// failure reports 0 instead of failing the whole status page.
func qualityEvidenceCount(ctx context.Context, st *store.Store, providerID string) int {
	count, err := st.CountEvidenceByProvider(ctx, providerID)
	if err != nil {
		return 0
	}
	return count
}

// qualityTimestamp renders a Unix-nano timestamp as RFC3339, or "" for zero.
// The frontend parses these fields with Date.parse, so an absent gate must be
// the empty string rather than 1970.
func qualityTimestamp(ns int64) string {
	if ns <= 0 {
		return ""
	}
	return time.Unix(0, ns).UTC().Format(time.RFC3339)
}

// qualitySeconds parses a Go duration string (the provider TTL wire form) into
// whole seconds.
func qualitySeconds(duration string) int64 {
	parsed, err := time.ParseDuration(strings.TrimSpace(duration))
	if err != nil || parsed <= 0 {
		return 0
	}
	return int64(parsed.Seconds())
}

// QualityIP returns one IP's quality summary from the intel rows.
func (s *ControlPlaneService) QualityIP(ctx context.Context, rawIP string) (quality.Summary, error) {
	ip, err := netip.ParseAddr(strings.TrimSpace(rawIP))
	if err != nil {
		return quality.Summary{}, invalidArg("ip: invalid address")
	}
	svc, serr := s.intelService()
	if serr != nil {
		return quality.Summary{}, serr
	}
	normalized := ip.Unmap().String()
	rows, err := svc.Store().ListEvidenceByIP(ctx, normalized)
	if err != nil {
		return quality.Summary{}, qualityStorageError("EVIDENCE_READ_FAILED", err)
	}
	assessment, err := s.qualityAssessmentRow(ctx, svc, normalized)
	if err != nil {
		return quality.Summary{}, err
	}
	return qualitySummaryFromRows(normalized, rows, assessment, time.Now().UTC(), s.qualityBands()), nil
}

// ListQuality returns every assessed IP, newest evidence first, optionally
// filtered by a substring of the IP, ASN, organisation or verdict.
//
// The evidence of the whole page is read in one batched query, so the endpoint
// stays a bounded number of queries regardless of how many rows are returned.
func (s *ControlPlaneService) ListQuality(ctx context.Context, query string) ([]quality.Summary, error) {
	svc, serr := s.intelService()
	if serr != nil {
		return nil, serr
	}
	st := svc.Store()
	rows, err := st.ListAssessments(ctx, maxQualityListRows, 0)
	if err != nil {
		return nil, internal("list assessments", err)
	}
	ips := make([]string, 0, len(rows))
	for _, row := range rows {
		ips = append(ips, row.IP)
	}
	evidence, err := st.ListEvidenceByIPs(ctx, ips)
	if err != nil {
		return nil, qualityStorageError("EVIDENCE_READ_FAILED", err)
	}
	byIP := make(map[string][]store.Evidence, len(ips))
	for _, row := range evidence {
		byIP[row.IP] = append(byIP[row.IP], row)
	}

	now := time.Now().UTC()
	query = strings.ToLower(strings.TrimSpace(query))
	result := make([]quality.Summary, 0, len(rows))
	for i := range rows {
		row := rows[i]
		summary := qualitySummaryFromRows(row.IP, byIP[row.IP], &row, now, s.qualityBands())
		if query != "" && !strings.Contains(strings.ToLower(qualityHaystack(summary)), query) {
			continue
		}
		result = append(result, summary)
	}
	// Newest evidence first, then IP: the order the legacy list documented.
	sort.SliceStable(result, func(i, j int) bool {
		a, b := qualityLatestEvidence(result[i]), qualityLatestEvidence(result[j])
		if !a.Equal(b) {
			return a.After(b)
		}
		return result[i].IP < result[j].IP
	})
	return result, nil
}

// maxQualityListRows bounds one GET /quality/assessments response.
const maxQualityListRows = 2000

// qualityLatestEvidence is the most recent observation of a summary, used for
// the list ordering. A summary without evidence sorts last.
func qualityLatestEvidence(summary quality.Summary) time.Time {
	var latest time.Time
	for _, source := range summary.Sources {
		if source.Evidence != nil && source.Evidence.ObservedAt.After(latest) {
			latest = source.Evidence.ObservedAt
		}
	}
	return latest
}

// qualityHaystack is the searchable text of one summary: the IP plus the
// organisation, ASN and verdict of every source that reported one.
func qualityHaystack(summary quality.Summary) string {
	parts := []string{summary.IP}
	for _, source := range summary.Sources {
		if source.Evidence != nil {
			parts = append(parts, source.Evidence.ASN, source.Evidence.Organization)
		}
	}
	if summary.Assessment != nil {
		parts = append(parts, summary.Assessment.Verdict, summary.Assessment.PurityBand, summary.Assessment.NetworkType)
		parts = append(parts, summary.Assessment.Reasons...)
		f := summary.Assessment.Facts["purity"]
		parts = append(parts, f.Cause, f.ErrorCode, f.Explanation)
	}
	return strings.Join(parts, " ")
}

// qualityAssessmentRow reads the authoritative assessment row of one IP.
func (s *ControlPlaneService) qualityAssessmentRow(ctx context.Context, svc *intel.Service, ip string) (*store.Assessment, error) {
	row, ok, err := svc.Store().GetAssessment(ctx, ip)
	if err != nil {
		return nil, qualityStorageError("EVIDENCE_READ_FAILED", err)
	}
	if !ok {
		return nil, nil
	}
	return &row, nil
}

// qualitySummaryFromRows maps the intel rows of one IP onto the legacy summary.
//
// It is a pure function so the projection can be unit-tested without a database.
func qualitySummaryFromRows(ip string, rows []store.Evidence, assessment *store.Assessment, now time.Time, configured ...quality.Bands) quality.Summary {
	bands := quality.DefaultBands()
	if len(configured) > 0 {
		bands = configured[0].Normalized()
	}
	summary := quality.Summary{
		IP:      ip,
		State:   qualityStateFromRows(rows, assessment, now),
		Sources: make([]quality.SourceSummary, 0, len(rows)),
	}
	for _, row := range rows {
		source := quality.SourceSummary{
			Provider:   row.Provider,
			Configured: true,
			State:      qualityStateFromEvidence(row, now),
		}
		if issue := qualityEvidenceIssue(row, now); issue != "" {
			source.Task = &quality.Task{IP: row.IP, Provider: row.Provider, State: "failed", ErrorCode: quality.SafeCode(issue)}
		}
		if ev, err := intel.EvidenceFromRow(row); err == nil {
			evidence := ev
			source.Evidence = &evidence
			if summary.Evidence == nil || row.Provider == quality.ProviderID {
				// The frontend labels summary.evidence as the ProxyCheck network
				// evidence, exactly as the legacy projection did.
				summary.Evidence = &evidence
			}
		}
		summary.Sources = append(summary.Sources, source)
	}
	// A failed provider keeps its last evidence and reports the error, which is
	// what the frontend renders through summary.task.error_code.
	for _, row := range rows {
		if row.Status == store.StatusError && strings.TrimSpace(row.ErrorCode) != "" {
			summary.Task = &quality.Task{
				IP: row.IP, Provider: row.Provider, Profile: row.Profile,
				State: "failed", ErrorCode: quality.SafeCode(row.ErrorCode),
			}
			break
		}
	}
	if assessment != nil {
		mapped := qualityAssessmentFromLite(intel.AssessmentLiteFromRow(*assessment, bands).Current(now))
		diagnostic := assessmentDiagnostics(ip, rows, now)
		if mapped.PurityScore != nil && (diagnostic.PurityScore == nil || *diagnostic.PurityScore != *mapped.PurityScore) {
			mapped.PurityScore = nil
			fact := diagnostic.Facts["purity"]
			if diagnostic.PurityScore != nil {
				fact = quality.PurityDiagnostic("CURRENT_EXIT_UNASSESSED")
			}
			mapped.Facts["purity"] = fact
			mapped.Reasons = append(mapped.Reasons, fact.Cause)
			mapped.State = "pending"
			if mapped.Verdict == "favorable" {
				mapped.Verdict = "pending"
			}
			summary.State = "pending"
		}
		mapped.PurityBand = bands.Name(mapped.PurityScore)
		mapped.Bands = bands
		mapped.NetworkSource = "assessment"
		mapped.TorRoles = qualityTorRoles(rows, now)
		summary.Assessment = &mapped
	}
	if summary.Assessment == nil {
		diagnostic := assessmentDiagnostics(ip, rows, now)
		f := diagnostic.Facts["purity"]
		if diagnostic.PurityScore != nil {
			f = quality.PurityDiagnostic("CURRENT_EXIT_UNASSESSED")
		}
		summary.Assessment = &quality.Assessment{State: "pending", ScoreSource: "ippure", PurityBand: "unknown", NetworkType: "unknown", Verdict: "pending", Reasons: []string{f.Cause}, Facts: map[string]quality.Fact{"purity": f}, Bands: bands}
	}
	return summary
}

// qualityTorRoles returns the public relay roles of the Tor registry evidence.
func qualityTorRoles(rows []store.Evidence, now time.Time) []string {
	for _, row := range rows {
		if row.Provider != "torproject" || qualityStateFromEvidence(row, now) != "valid" {
			continue
		}
		ev, err := intel.EvidenceFromRow(row)
		if err != nil || len(ev.TorRoles) == 0 {
			continue
		}
		return append([]string(nil), ev.TorRoles...)
	}
	return nil
}

// qualityStateFromRows resolves the summary state of one IP.
//
// The vocabulary is the legacy one (types.ts QualityState). "partial" is no
// longer produced: the intel assessment states its own coverage through
// confidence and reasons, so an incomplete assessment is reported as pending or
// valid rather than as a fourth state.
func qualityStateFromRows(rows []store.Evidence, assessment *store.Assessment, now time.Time) string {
	if assessment != nil {
		return qualityStateFromLite(intel.AssessmentLiteFromRow(*assessment), now)
	}
	if len(rows) == 0 {
		return "unobserved"
	}
	state := "unobserved"
	for _, row := range rows {
		switch qualityStateFromEvidence(row, now) {
		case "valid":
			return "valid"
		case "stale":
			state = "stale"
		case "pending":
			if state == "unobserved" {
				state = "pending"
			}
		}
	}
	return state
}

func parseQualityEvidenceIP(raw string) netip.Addr { ip, _ := netip.ParseAddr(raw); return ip }

// Keep invalid persisted observations explainable without exposing payloads.
func qualityEvidenceIssue(row store.Evidence, now time.Time) string {
	if row.ErrorCode != "" {
		return quality.SafeCode(row.ErrorCode)
	}
	if row.Status != store.StatusOk {
		return ""
	}
	ev, err := intel.EvidenceFromRow(row)
	if err != nil {
		return "EVIDENCE_DECODE_FAILED"
	}
	if ev.IP != row.IP {
		return "EGRESS_MISMATCH"
	}
	if row.ObservedAtNs <= 0 || row.ObservedAtNs > now.UnixNano() || ev.ObservedAt.IsZero() || ev.ObservedAt.After(now) || ev.ValidUntil.IsZero() {
		return "EVIDENCE_TIME_INVALID"
	}
	if row.ValidUntilNs <= now.UnixNano() || !ev.ValidUntil.After(now) {
		return "EVIDENCE_EXPIRED"
	}
	return ""
}

// qualityStateFromEvidence maps one intel evidence status onto the legacy state.
func qualityStateFromEvidence(row store.Evidence, now time.Time) string {
	switch row.Status {
	case store.StatusOk:
		if row.ObservedAtNs <= 0 || row.ObservedAtNs > now.UnixNano() {
			return "unobserved"
		}
		if row.ValidUntilNs <= now.UnixNano() {
			return "stale"
		}
		ev, err := intel.EvidenceFromRow(row)
		if err != nil || len(assess.CurrentEvidence(parseQualityEvidenceIP(row.IP), []quality.Evidence{ev}, now)) == 0 {
			return "unobserved"
		}
		return "valid"
	case store.StatusUnsupported:
		return "unsupported"
	case store.StatusError:
		// A provider error is not an answer: the source stays unobserved and the
		// error travels in summary.task.error_code instead.
		return "unobserved"
	default:
		return "pending"
	}
}

// qualityScorePtr narrows the stored purity score to the *int the legacy shape
// uses. The column is an INTEGER; the legacy contract is a small 0..100 value.
func qualityScorePtr(score *int64) *int {
	if score == nil || *score < 0 || *score > 100 {
		return nil
	}
	value := int(*score)
	return &value
}

// qualityAssessmentFromRow maps an intel assessment row onto the legacy shape.
//
// The mapping is field-for-field because both sides describe the same computed
// result; the intel row is the source of truth since replaced the old
// in-memory assessment.
func qualityAssessmentFromRow(row store.Assessment) quality.Assessment {
	return qualityAssessmentFromLite(intel.AssessmentLiteFromRow(row).Current(time.Now().UTC()))
}

// qualityAssessmentFromLite maps the in-memory projection onto the legacy shape.
//
// Reasons are deliberately empty: the projection keeps a flag bitmask rather
// than the scorer's reason strings, and inventing reasons from flags would show
// the frontend codes it cannot translate.
func qualityAssessmentFromLite(lite intel.AssessmentLite) quality.Assessment {
	return quality.Assessment{
		State:       intel.StateName(lite.State),
		ScoreSource: "ippure",
		Facts:       lite.FactValues(),
		PurityScore: lite.ScoreValue(),
		PurityBand:  intel.BandName(lite.Band),
		NetworkType: intel.IPTypeName(lite.IPType),
		Native:      lite.NativeValue(),
		Verdict:     intel.VerdictName(lite.Verdict),
		// A nil slice marshals to `null`, and "deliberately empty" has to look like
		// an empty list on the wire. The WebUI reads this array, and the sibling
		// qualityAssessmentFromRow path already yields [] from the stored JSON.
		Reasons: lite.ReasonValues(),
	}
}

// qualityStateFromLite maps the projection onto the legacy summary state.
func qualityStateFromLite(lite intel.AssessmentLite, now time.Time) string {
	if !lite.Valid(now.UnixNano()) {
		return "stale"
	}
	if intel.IPTypeName(lite.IPType) == assessmentIPTypeConflict ||
		intel.VerdictName(lite.Verdict) == assessmentVerdictConflict {
		return assessmentVerdictConflict
	}
	switch lite.State {
	case intel.StateUnsupported:
		return "unsupported"
	case intel.StateValid:
		return "valid"
	default:
		return "pending"
	}
}

// nodeQuality projects the quality summary of one egress IP from the in-memory
// intel projection.
//
// It performs no database read: the node list calls it once per matched node, so
// it must stay an allocation-only lookup (R3). The evidence rows that carry the
// per-provider detail are added for a whole page at once by
// FillNodeQualityEvidence.
func (s *ControlPlaneService) nodeQuality(ip netip.Addr, nodeHashes ...string) quality.Summary {
	summary := quality.Summary{State: "unobserved", Sources: []quality.SourceSummary{}}
	if !ip.IsValid() {
		return summary
	}
	summary.IP = ip.Unmap().String()
	snap := s.intelProjection()
	if snap == nil {
		return summary
	}
	lite, ok := snap.Assessment(ip.Unmap())
	if len(nodeHashes) > 0 {
		lite, ok = snap.NodeAssessment(nodeHashes[0], ip.Unmap())
	}
	if !ok {
		return summary
	}
	assessment := qualityAssessmentFromLite(lite.Current(time.Now().UTC()))
	assessment.Bands = s.qualityBands()
	summary.Assessment = &assessment
	summary.State = qualityStateFromLite(lite, time.Now().UTC())
	return summary
}

// maxQualityEvidenceIPs bounds one batched evidence lookup. A page of nodes is
// far smaller; the bound keeps a caller that passes the whole pool from turning
// into an unbounded query.
const maxQualityEvidenceIPs = 2000

// FillNodeQualityEvidence adds the per-provider evidence of a page of nodes in
// one batched query.
//
// The node list and the node detail both call it right after
// FillNodeIntelEgress: the assessment already came from the projection, and this
// is the only database read the quality view needs.
func (s *ControlPlaneService) FillNodeQualityEvidence(ctx context.Context, nodes []NodeSummary) {
	defer func() {
		for i := range nodes {
			nodes[i].Intel.explainFacts()
			if a := nodes[i].Quality.Assessment; a != nil {
				a.Facts = nodes[i].Intel.Facts
				a.PurityScore = nodes[i].Intel.PurityScore
				a.PurityBand = nodes[i].Intel.PurityBand
				a.Verdict = nodes[i].Intel.Verdict
			}
			if nodes[i].ReferenceLatencyMs == nil {
				nodes[i].Intel.Facts["latency"] = quality.Missing("active_probe", "NO_SUCCESSFUL_LATENCY_SAMPLE", "修复节点连接后运行延迟探测；失败继续健康重试，不删除节点。")
			} else {
				nodes[i].Intel.Facts["latency"] = quality.Known("active_probe")
			}
		}
	}()
	if s == nil || s.Intel == nil || s.Intel.Store() == nil || len(nodes) == 0 {
		return
	}
	ips := make([]string, 0, len(nodes))
	for i := range nodes {
		if nodes[i].EgressIP != "" {
			ips = append(ips, nodes[i].EgressIP)
		}
		if len(ips) >= maxQualityEvidenceIPs {
			break
		}
	}
	if len(ips) == 0 {
		return
	}
	rows, err := s.Intel.Store().ListEvidenceByIPs(ctx, ips)
	if err != nil {
		for i := range nodes {
			nodes[i].Intel.evidenceReadFailed = true
		}
		return
	}
	byIP := make(map[string][]store.Evidence, len(ips))
	for _, row := range rows {
		byIP[row.IP] = append(byIP[row.IP], row)
	}
	now := time.Now().UTC()
	for i := range nodes {
		evidence := byIP[nodes[i].EgressIP]
		diagnostic := assessmentDiagnostics(nodes[i].EgressIP, evidence, now)
		if diagnostic.PurityScore == nil && len(evidence) > 0 {
			nodes[i].Intel.PurityScore = nil
			nodes[i].Intel.PurityBand = "unknown"
			if nodes[i].Intel.Facts == nil {
				nodes[i].Intel.Facts = map[string]quality.Fact{}
			}
			nodes[i].Intel.Facts["purity"] = diagnostic.Facts["purity"]
		}
		sources := make([]quality.SourceSummary, 0, len(evidence))
		if nodes[i].Intel.Facts == nil {
			nodes[i].Intel.explainFacts()
		}
		for _, key := range []string{"purity", "network_type", "proxy", "vpn", "tor", "hosting", "compromised", "scraper", "anonymous", "asn", "as_org", "country", "city", "native"} {
			f := nodes[i].Intel.Facts[key]
			if f.Provider != "" || len(f.Observations) > 0 {
				continue
			}
			f.Sources = nil
			for _, row := range evidence {
				source := row.Provider + ":" + qualityStateFromEvidence(row, now)
				if issue := qualityEvidenceIssue(row, now); issue != "" {
					source += ":" + issue
				}
				if ev, err := intel.EvidenceFromRow(row); err == nil && key == "network_type" && ev.IPType != "" {
					source += ":" + ev.IPType
				}
				f.Sources = append(f.Sources, source)
			}
			nodes[i].Intel.Facts[key] = f
		}
		var primary *quality.Evidence
		var candidates []quality.Evidence
		for _, row := range evidence {
			source := quality.SourceSummary{
				Provider:   row.Provider,
				Configured: true,
				State:      qualityStateFromEvidence(row, now),
			}
			if issue := qualityEvidenceIssue(row, now); issue != "" {
				source.Task = &quality.Task{IP: row.IP, Provider: row.Provider, State: "failed", ErrorCode: quality.SafeCode(issue)}
			}
			if ev, err := intel.EvidenceFromRow(row); err == nil {
				copied := ev
				source.Evidence = &copied
				if row.Status == store.StatusOk {
					candidates = append(candidates, ev)
				}

				if primary == nil || row.Provider == quality.ProviderID {
					primary = &copied
				}
			}
			sources = append(sources, source)
		}
		nodes[i].Intel.Signals = quality.Signals{}
		for _, ev := range assess.CurrentEvidence(parseQualityEvidenceIP(nodes[i].EgressIP), candidates, now) {
			mergeMeasuredSignal(&nodes[i].Intel.Signals.Proxy, ev.Signals.Proxy)
			mergeMeasuredSignal(&nodes[i].Intel.Signals.VPN, ev.Signals.VPN)
			mergeMeasuredSignal(&nodes[i].Intel.Signals.Tor, ev.Signals.Tor)
			mergeMeasuredSignal(&nodes[i].Intel.Signals.Hosting, ev.Signals.Hosting)
			mergeMeasuredSignal(&nodes[i].Intel.Signals.Compromised, ev.Signals.Compromised)
			mergeMeasuredSignal(&nodes[i].Intel.Signals.Scraper, ev.Signals.Scraper)
			mergeMeasuredSignal(&nodes[i].Intel.Signals.Anonymous, ev.Signals.Anonymous)
		}
		nodes[i].Quality.Sources = sources
		nodes[i].Quality.Evidence = primary
		if nodes[i].Quality.Assessment != nil {
			nodes[i].Quality.Assessment.NetworkSource = "assessment"
			nodes[i].Quality.Assessment.TorRoles = qualityTorRoles(evidence, now)
		}
	}
}

// RequestIPQuality force-enqueues one IP on every runnable online source
//
// It returns the refreshed summary and whether anything was queued; the handler
// answers 202 when it was.
func (s *ControlPlaneService) RequestIPQuality(ctx context.Context, rawIP string) (RequestResult, error) {
	ip, err := netip.ParseAddr(strings.TrimSpace(rawIP))
	if err != nil {
		return RequestResult{}, invalidArg("ip: invalid address")
	}
	// The legacy endpoint only inspected public addresses: a provider lookup of
	// a private or reserved address is a caller mistake, not a provider failure.
	public, err := quality.PublicIP(ip)
	if err != nil {
		return RequestResult{}, invalidArg("quality inspection requires a public IP address")
	}
	svc, serr := s.intelService()
	if serr != nil {
		return RequestResult{}, serr
	}
	if !svc.Manager().Enabled() {
		return RequestResult{}, conflict("quality inspection is disabled")
	}
	normalized := public.String()
	queued, err := s.enqueueQualityIP(ctx, svc, normalized)
	if err != nil {
		return RequestResult{}, err
	}
	summary, err := s.QualityIP(ctx, normalized)
	if err != nil {
		return RequestResult{}, err
	}
	return RequestResult{Quality: summary, Queued: queued}, nil
}

// enqueueQualityIP force-enqueues one IP on every runnable online provider and
// reports whether any queue row was touched.
//
// Priority 100 is what specifies for a manual request, so it outranks
// the automatic pipeline work. When no online source can run at all the caller
// gets the legacy "quality inspection is disabled" conflict instead of a 202
// that promises work nobody will do.
func (s *ControlPlaneService) enqueueQualityIP(ctx context.Context, svc *intel.Service, ip string) (bool, error) {
	now := time.Now().UTC().UnixNano()
	queued := false
	runnable := 0
	for _, setting := range svc.ProviderSettings().Registry().Settings() {
		if setting.Spec.Kind != providers.KindOnlineIP || !setting.Runnable() {
			continue
		}
		runnable++
		if _, err := svc.Store().EnqueueProviderItem(ctx, store.QueueItem{
			Provider:     setting.Spec.ID,
			IP:           ip,
			Priority:     manualQualityPriority,
			Status:       store.QueueQueued,
			NextRunAtNs:  now,
			EnqueuedAtNs: now,
		}, store.EnqueueForce); err != nil {
			return queued, internal("enqueue provider lookup", err)
		}
		// EnqueueForce resets an existing row back to queued, so a touched row
		// counts as queued whether or not it was newly inserted.
		queued = true
	}
	if runnable == 0 {
		return false, conflict("quality inspection is disabled")
	}
	return queued, nil
}

// manualQualityPriority is the queue priority of a user-requested lookup.
const manualQualityPriority = 100

// ProbeQuality creates an intel job for one node (the legacy
// POST /nodes/{hash}/actions/probe-quality) and returns its id.
func (s *ControlPlaneService) ProbeQuality(ctx context.Context, hash string) (RequestResult, error) {
	h, err := node.ParseHex(hash)
	if err != nil {
		return RequestResult{}, invalidArg("node_hash: invalid format")
	}
	if s == nil || s.Pool == nil {
		return RequestResult{}, conflict("intel subsystem is not available")
	}
	if _, ok := s.Pool.GetEntry(h); !ok {
		return RequestResult{}, notFound("node not found")
	}
	job, err := s.CreateIntelJob(ctx, IntelJobRequest{
		Kind:  jobs.KindIntel,
		Scope: jobs.Scope{NodeHashes: []string{hash}},
		Force: true,
	})
	if err != nil {
		return RequestResult{}, err
	}
	return RequestResult{Queued: true, JobID: job.ID}, nil
}

// ReviewIPPure creates an intel job that reviews one node through IPPure and
// waits briefly for its evidence (the legacy
// POST /nodes/{hash}/actions/review-ippure).
//
// The wait is bounded by ippureReviewWait. A caller that wants to wait longer
// polls the returned job; the response says so through Queued.
func (s *ControlPlaneService) ReviewIPPure(ctx context.Context, hash string) (IPPureReviewResult, error) {
	h, err := node.ParseHex(hash)
	if err != nil {
		return IPPureReviewResult{}, invalidArg("node_hash: invalid format")
	}
	if s == nil || s.Pool == nil {
		return IPPureReviewResult{}, conflict("intel subsystem is not available")
	}
	entry, ok := s.Pool.GetEntry(h)
	if !ok {
		return IPPureReviewResult{}, notFound("node not found")
	}
	expected := entry.GetEgressIP().Unmap()

	job, err := s.CreateIntelJob(ctx, IntelJobRequest{
		Kind:      jobs.KindIntel,
		Scope:     jobs.Scope{NodeHashes: []string{hash}},
		Providers: []string{ippureProviderID},
		Force:     true,
	})
	if err != nil {
		return IPPureReviewResult{}, err
	}

	now := time.Now().UTC()
	result := IPPureReviewResult{
		NodeHash: hash,
		JobID:    job.ID,
		Queued:   true,
	}
	if expected.IsValid() {
		result.ExpectedIP = expected.String()
	}

	// Poll the evidence the job is producing. The window is short on purpose: a
	// caller that wants to wait longer polls the job itself.
	deadline := now.Add(ippureReviewWait)
	for {
		if expected.IsValid() {
			evidence, err := s.ippureEvidence(ctx, expected.String())
			if err != nil {
				return IPPureReviewResult{}, err
			}
			if evidence != nil {
				result.Evidence = evidence
				result.Queued = false
				result.IsResidential = evidence.IsResidential
				// IPPure does not calculate an IPv6 fraud score.
				result.ScoreSupported = expected.Is4() && evidence.FraudScore != nil
				result.MatchesNodeIP = evidence.IP == expected.String() && s.nodeEgressStillIs(h, expected)
				return result, nil
			}
		}
		if !time.Now().Before(deadline) {
			return result, nil
		}
		select {
		case <-ctx.Done():
			return result, nil
		case <-time.After(ippureReviewPoll):
		}
	}
}

// nodeEgressStillIs reports whether the node's egress IP is still the one the
// review started with. A per-target exit can differ from the cached egress; such
// a result is returned but never marked as matching the node's own IP.
func (s *ControlPlaneService) nodeEgressStillIs(hash node.Hash, expected netip.Addr) bool {
	if s == nil || s.Pool == nil {
		return false
	}
	entry, ok := s.Pool.GetEntry(hash)
	return ok && entry.GetEgressIP().Unmap() == expected
}

// IPPureReviewResult is the POST /nodes/{hash}/actions/review-ippure body. The
// field names are the ones internal/api/web/src/features/quality/types.ts
// declares (IPPureReview); job_id and queued are additive.
type IPPureReviewResult struct {
	Evidence       *quality.Evidence `json:"evidence"`
	NodeHash       string            `json:"node_hash"`
	ExpectedIP     string            `json:"expected_ip"`
	MatchesNodeIP  bool              `json:"matches_node_ip"`
	IsResidential  *bool             `json:"is_residential"`
	ScoreSupported bool              `json:"score_supported"`
	NextAllowedAt  string            `json:"next_allowed_at"`
	JobID          string            `json:"job_id,omitempty"`
	// Queued reports that the evidence was not ready within the wait window and
	// the job is still running (the handler answers 202 then).
	Queued bool `json:"queued"`
}

// ippureReviewWait bounds how long ReviewIPPure waits for the evidence.
const ippureReviewWait = 15 * time.Second

// ippureReviewPoll is the evidence polling interval during that wait.
const ippureReviewPoll = 250 * time.Millisecond

// ippureProviderID is the provider whose evidence the review reports.
const ippureProviderID = "ippure"

// ippureEvidence returns the fresh IPPure evidence of one IP, or nil when there
// is none yet.
func (s *ControlPlaneService) ippureEvidence(ctx context.Context, ip string) (*quality.Evidence, error) {
	svc, serr := s.intelService()
	if serr != nil {
		return nil, serr
	}
	row, ok, err := svc.Store().GetEvidence(ctx, ip, ippureProviderID)
	if err != nil {
		return nil, qualityStorageError("EVIDENCE_READ_FAILED", err)
	}
	if !ok || row.Status != store.StatusOk {
		return nil, nil
	}
	if row.ValidUntilNs > 0 && row.ValidUntilNs <= time.Now().UTC().UnixNano() {
		// An expired row is not an answer to a fresh request.
		return nil, nil
	}
	ev, err := intel.EvidenceFromRow(row)
	if err != nil {
		// A cached row that cannot be decoded is a broken record, not an
		// absence of evidence: answering nil would tell the caller the address
		// had never been inspected.
		return nil, internal("decode evidence", err)
	}
	if len(assess.CurrentEvidence(parseQualityEvidenceIP(ip), []quality.Evidence{ev}, time.Now().UTC())) == 0 {
		return nil, nil
	}
	return &ev, nil
}

// Missing signals stay nil. An observed positive outranks observed negatives.
func mergeMeasuredSignal(dst **bool, src *bool) {
	if src == nil {
		return
	}
	if *dst == nil || *src {
		v := *src
		*dst = &v
	}
}

// qualityBands is the single runtime band policy, also installed on the intel
// snapshot for filters, routing, export and job scopes.
func (s *ControlPlaneService) qualityBands() quality.Bands {
	if s != nil && s.RuntimeCfg != nil {
		if cfg := s.RuntimeCfg.Load(); cfg != nil {
			b := cfg.PurityBands.Normalized()
			return quality.Bands{Excellent: b.Excellent, Clean: b.Clean, Fair: b.Fair, Mixed: b.Mixed}
		}
	}
	return quality.DefaultBands()
}

// assessmentDiagnostics uses the same pure assessor as persisted evaluations.
// Callers use it only to reject stale scores and explain absent assessments.
func assessmentDiagnostics(ip string, rows []store.Evidence, now time.Time) assess.Assessment {
	input := intel.AssessmentInputFromRows(parseQualityEvidenceIP(ip), rows, now)
	return assess.AssessInput(input)
}
