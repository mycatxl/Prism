package store

import (
	"database/sql"
	"strconv"
	"strings"
)

// Status values shared by evidence, queue and job rows.
const (
	StatusOk          = "ok"
	StatusError       = "error"
	StatusUnsupported = "unsupported"

	QueueQueued  = "queued"
	QueueRunning = "running"
	QueueDone    = "done"
	QueueFailed  = "failed"

	ItemQueued   = "queued"
	ItemRunning  = "running"
	ItemDone     = "done"
	ItemFailed   = "failed"
	ItemSkipped  = "skipped"
	ItemCanceled = "canceled"

	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobPartial   = "partial"
	JobFailed    = "failed"
	JobCanceled  = "canceled"
)

// MaxNormalizedJSONBytes caps the normalised quality.Evidence blob (§2: 8 KiB).
const MaxNormalizedJSONBytes = 8 * 1024

// MaxRawJSONBytes caps a stored provider response (§2: 32 KiB).
const MaxRawJSONBytes = 32 * 1024

// MaxResultJSONBytes caps the per-step job item summary (§2: 4 KiB).
const MaxResultJSONBytes = 4 * 1024

// MaxDetailJSONBytes caps a node check detail blob (§2: 4 KiB).
const MaxDetailJSONBytes = 4 * 1024

// EgressFamilies are the two address families tracked by node_egress.
const (
	FamilyV4 = 4
	FamilyV6 = 6
)

// NodeEgress is one row of node_egress: the last observed egress addresses of a
// node plus the Cloudflare colo/loc metadata.
type NodeEgress struct {
	NodeHash     string `json:"node_hash"`
	IPv4         string `json:"ipv4"`
	IPv6         string `json:"ipv6"`
	Colo         string `json:"colo"`
	Loc          string `json:"loc"`
	V4ObservedNs int64  `json:"v4_observed_ns"`
	V6ObservedNs int64  `json:"v6_observed_ns"`
	V6CheckedNs  int64  `json:"v6_checked_ns"`
}

// EgressHistory is one row of egress_history (an IP change record).
type EgressHistory struct {
	ID           int64  `json:"id"`
	NodeHash     string `json:"node_hash"`
	Family       int    `json:"family"`
	IP           string `json:"ip"`
	ObservedAtNs int64  `json:"observed_at_ns"`
}

// Evidence is one row of the evidence table: a normalised provider conclusion
// about one IP.
type Evidence struct {
	IP             string         `json:"ip"`
	Provider       string         `json:"provider"`
	Profile        string         `json:"profile"`
	ViaNodeHash    string         `json:"via_node_hash"`
	Status         string         `json:"status"`
	ObservedAtNs   int64          `json:"observed_at_ns"`
	ValidUntilNs   int64          `json:"valid_until_ns"`
	NormalizedJSON string         `json:"normalized_json"`
	RawJSON        sql.NullString `json:"-"`
	ErrorCode      string         `json:"error_code"`
}

// Raw returns the stored raw payload or an empty byte slice.
func (e Evidence) Raw() []byte {
	if !e.RawJSON.Valid {
		return nil
	}
	return []byte(e.RawJSON.String)
}

// NodeCheck is one row of node_checks: an unlock/availability result observed
// through a node's own egress.
type NodeCheck struct {
	NodeHash     string `json:"node_hash"`
	CheckID      string `json:"check_id"`
	CheckVersion int    `json:"check_version"`
	EgressIP     string `json:"egress_ip"`
	Outcome      string `json:"outcome"`
	Region       string `json:"region"`
	DetailJSON   string `json:"detail_json"`
	LatencyMs    int    `json:"latency_ms"`
	ObservedAtNs int64  `json:"observed_at_ns"`
	ValidUntilNs int64  `json:"valid_until_ns"`
}

// Assessment is one row of ip_assessment. Nullable columns use sql.Null*
// because "unknown" is a meaningful value for purity_score, native and asn.
type Assessment struct {
	IP             string        `json:"ip"`
	Profile        string        `json:"profile"`
	State          string        `json:"state"`
	Verdict        string        `json:"verdict"`
	PurityScore    sql.NullInt64 `json:"-"`
	PurityBand     string        `json:"purity_band"`
	Confidence     string        `json:"confidence"`
	Coverage       float64       `json:"coverage"`
	IPType         string        `json:"ip_type"`
	Native         sql.NullInt64 `json:"-"`
	Flags          int64         `json:"flags"`
	ASN            sql.NullInt64 `json:"-"`
	ASOrg          string        `json:"as_org"`
	Country        string        `json:"country"`
	City           string        `json:"city"`
	ReasonsJSON    string        `json:"reasons_json"`
	ComponentsJSON string        `json:"components_json"`
	ComputedAtNs   int64         `json:"computed_at_ns"`
	ValidUntilNs   int64         `json:"valid_until_ns"`
}

// ScoreOrNil returns the purity score pointer (nil when unknown).
func (a Assessment) ScoreOrNil() *int64 {
	if !a.PurityScore.Valid {
		return nil
	}
	v := a.PurityScore.Int64
	return &v
}

// NativeOrNil returns the native flag pointer (nil when unknown).
func (a Assessment) NativeOrNil() *int64 {
	if !a.Native.Valid {
		return nil
	}
	v := a.Native.Int64
	return &v
}

// ASNOrNil returns the ASN pointer (nil when unknown).
func (a Assessment) ASNOrNil() *int64 {
	if !a.ASN.Valid {
		return nil
	}
	v := a.ASN.Int64
	return &v
}

// ProviderState is one row of provider_state: the persisted budget, rate limit
// and pause state of one data source.
type ProviderState struct {
	Provider        string `json:"provider"`
	Day             string `json:"day"`
	Used            int    `json:"used"`
	NextRequestAtNs int64  `json:"next_request_at_ns"`
	BlockedUntilNs  int64  `json:"blocked_until_ns"`
	Paused          bool   `json:"paused"`
	ErrorCode       string `json:"error_code"`
	CredentialID    string `json:"credential_id"`
}

// QueueItem is one row of provider_queue: a de-duplicated online lookup task.
type QueueItem struct {
	Provider     string `json:"provider"`
	IP           string `json:"ip"`
	Priority     int    `json:"priority"`
	JobID        string `json:"job_id"`
	Status       string `json:"status"`
	Attempts     int    `json:"attempts"`
	NextRunAtNs  int64  `json:"next_run_at_ns"`
	LeaseOwner   string `json:"-"`
	LeaseUntilNs int64  `json:"-"`
	ErrorCode    string `json:"error_code"`
	EnqueuedAtNs int64  `json:"enqueued_at_ns"`
}

// Job is one row of the jobs table.
type Job struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Status       string `json:"status"`
	Priority     int    `json:"priority"`
	RequestJSON  string `json:"request_json"`
	Total        int    `json:"total"`
	Done         int    `json:"done"`
	Failed       int    `json:"failed"`
	Skipped      int    `json:"skipped"`
	CreatedBy    string `json:"created_by"`
	CreatedAtNs  int64  `json:"created_at_ns"`
	StartedAtNs  int64  `json:"started_at_ns"`
	FinishedAtNs int64  `json:"finished_at_ns"`
	Error        string `json:"error"`
}

// Active reports whether the job still occupies a worker slot.
func (j Job) Active() bool {
	return j.Status == JobQueued || j.Status == JobRunning
}

// Terminal reports whether the job reached a final status.
func (j Job) Terminal() bool {
	switch j.Status {
	case JobSucceeded, JobPartial, JobFailed, JobCanceled:
		return true
	default:
		return false
	}
}

// JobItem is one row of job_items: the per-node progress of one job.
type JobItem struct {
	JobID        string `json:"job_id"`
	NodeHash     string `json:"node_hash"`
	Status       string `json:"status"`
	StepIndex    int    `json:"step_index"`
	Attempts     int    `json:"attempts"`
	NextRunAtNs  int64  `json:"next_run_at_ns"`
	LeaseOwner   string `json:"-"`
	LeaseUntilNs int64  `json:"-"`
	ResultJSON   string `json:"result_json"`
	ErrorCode    string `json:"error_code"`
	UpdatedAtNs  int64  `json:"updated_at_ns"`
}

// JobProgress is the aggregated counter view of one job.
type JobProgress struct {
	JobID              string `json:"job_id"`
	Status             string `json:"status"`
	Total              int    `json:"total"`
	Done               int    `json:"done"`
	Failed             int    `json:"failed"`
	Skipped            int    `json:"skipped"`
	Canceled           int    `json:"canceled"`
	Pending            int    `json:"pending"`
	PendingOnlineItems int    `json:"pending_online_lookups"`
}

// truncateJSON bounds a JSON blob to max bytes while keeping valid JSON when the
// input was valid; oversized values are replaced by a small marker object.
func truncateJSON(raw string, max int) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "{}"
	}
	if len(trimmed) <= max {
		return trimmed
	}
	return `{"truncated":true,"original_bytes":` + strconv.Itoa(len(trimmed)) + `}`
}

// truncateRaw bounds a stored provider response. An empty value means "do not
// store" (the provider setting store_raw=false).
func truncateRaw(raw string, max int) sql.NullString {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return sql.NullString{}
	}
	if len(trimmed) > max {
		trimmed = trimmed[:max]
	}
	return sql.NullString{String: trimmed, Valid: true}
}
