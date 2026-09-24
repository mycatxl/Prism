package service

import (
	"context"
	"errors"
	"net/netip"

	"prism/internal/intel"
	"prism/internal/intel/jobs"
	"prism/internal/intel/store"
)

// IntelJobRequest is the POST /api/v1/intel/jobs body (WP08 §3.1).
type IntelJobRequest = jobs.Request

// IntelJobDetail is the GET /api/v1/intel/jobs/{id} response.
type IntelJobDetail struct {
	Job      store.Job         `json:"job"`
	Progress store.JobProgress `json:"progress"`
}

// IntelProviderStatus is one entry of GET /api/v1/intel/status. WP09 adds the
// provider spec and the effective settings (never the API key).
type IntelProviderStatus struct {
	Provider        string  `json:"provider"`
	Day             string  `json:"day"`
	Used            int     `json:"used"`
	NextRequestAtNs int64   `json:"next_request_at_ns"`
	BlockedUntilNs  int64   `json:"blocked_until_ns"`
	Paused          bool    `json:"paused"`
	ErrorCode       string  `json:"error_code"`
	HasCredential   bool    `json:"has_key"`
	CredentialID    string  `json:"-"`
	Queued          int     `json:"queued"`
	Running         int     `json:"running"`
	Done            int     `json:"done"`
	Failed          int     `json:"failed"`
	Spec            *any    `json:"spec,omitempty"`
	DailyLimit      int     `json:"daily_limit,omitempty"`
	QPS             float64 `json:"qps,omitempty"`
}

// IntelStatus is the GET /api/v1/intel/status response.
type IntelStatus struct {
	Enabled    bool                  `json:"enabled"`
	Database   IntelDatabaseStatus   `json:"database"`
	Executor   jobs.Stats            `json:"executor"`
	Discarded  IntelDiscarded        `json:"discarded"`
	Providers  []IntelProviderStatus `json:"providers"`
	JobsByStat map[string]int        `json:"jobs_by_status"`
}

// IntelDatabaseStatus reports the intel.db footprint (WP08 §7).
type IntelDatabaseStatus struct {
	FileBytes   int64 `json:"db_bytes"`
	Assessments int   `json:"assessments"`
	NodeRecords int   `json:"node_egress"`
}

// IntelDiscarded reports the bounded queues' drop counters (R4).
type IntelDiscarded struct {
	EgressObservations int64 `json:"egress_observations"`
	SSEFrames          int64 `json:"sse_frames"`
}

// IntelJobItemPage is one page of GET /api/v1/intel/jobs/{id}/items.
type IntelJobItemPage struct {
	Items    []store.JobItem `json:"items"`
	Total    int             `json:"total"`
	JobID    string          `json:"job_id"`
	JobState string          `json:"job_status"`
}

// IntelNodeDetail is the GET /api/v1/intel/nodes/{hash} response.
type IntelNodeDetail struct {
	NodeHash    string                `json:"node_hash"`
	Egress      store.NodeEgress      `json:"egress"`
	History     []store.EgressHistory `json:"history"`
	Checks      []store.NodeCheck     `json:"checks"`
	IPv4        *store.Assessment     `json:"ipv4_assessment,omitempty"`
	IPv6        *store.Assessment     `json:"ipv6_assessment,omitempty"`
	V4Projected *intel.AssessmentLite `json:"ipv4_projection,omitempty"`
	V6Projected *intel.AssessmentLite `json:"ipv6_projection,omitempty"`
}

// IntelIPDetail is the GET /api/v1/intel/ip/{ip} response.
type IntelIPDetail struct {
	IP         string                `json:"ip"`
	Evidence   []store.Evidence      `json:"evidence"`
	Assessment *store.Assessment     `json:"assessment,omitempty"`
	Projection *intel.AssessmentLite `json:"projection,omitempty"`
	Nodes      []string              `json:"nodes"`
}

// maxIntelIPNodes caps the node list of the IP detail view (WP08 §7).
const maxIntelIPNodes = 100

// intelService returns the intel facade, or a CONFLICT error when WP08 is not
// wired into this build.
func (s *ControlPlaneService) intelService() (*intel.Service, *ServiceError) {
	if s == nil || s.Intel == nil || s.Intel.Store() == nil {
		return nil, conflict("intel subsystem is not available")
	}
	return s.Intel, nil
}

// CreateIntelJob persists a batch job and returns 202 {job}.
func (s *ControlPlaneService) CreateIntelJob(ctx context.Context, req IntelJobRequest) (store.Job, error) {
	svc, serr := s.intelService()
	if serr != nil {
		return store.Job{}, serr
	}
	job, err := svc.CreateJob(ctx, req, jobs.CreatedByAdmin(), jobs.PriorityManual)
	if err != nil {
		return store.Job{}, mapIntelError(err)
	}
	return job, nil
}

// ListIntelJobs returns one page of jobs plus the total count.
func (s *ControlPlaneService) ListIntelJobs(ctx context.Context, status string, limit, offset int) ([]store.Job, int, error) {
	svc, serr := s.intelService()
	if serr != nil {
		return nil, 0, serr
	}
	statusFilter := normalizeJobStatus(status)
	jobsList, err := svc.Manager().Store().ListJobs(ctx, statusFilter, limit, offset)
	if err != nil {
		return nil, 0, internal("list intel jobs", err)
	}
	total, err := svc.Manager().Store().CountJobs(ctx, statusFilter)
	if err != nil {
		return nil, 0, internal("count intel jobs", err)
	}
	return jobsList, total, nil
}

// GetIntelJob returns one job with its counters.
func (s *ControlPlaneService) GetIntelJob(ctx context.Context, jobID string) (IntelJobDetail, error) {
	svc, serr := s.intelService()
	if serr != nil {
		return IntelJobDetail{}, serr
	}
	job, err := svc.Manager().Store().GetJob(ctx, jobID)
	if err != nil {
		return IntelJobDetail{}, mapIntelError(err)
	}
	progress, err := svc.Manager().Progress(ctx, jobID)
	if err != nil {
		return IntelJobDetail{}, internal("read intel job progress", err)
	}
	return IntelJobDetail{Job: job, Progress: progress}, nil
}

// ListIntelJobItems returns one page of the job's node items.
func (s *ControlPlaneService) ListIntelJobItems(ctx context.Context, jobID, status string, limit, offset int) (IntelJobItemPage, error) {
	svc, serr := s.intelService()
	if serr != nil {
		return IntelJobItemPage{}, serr
	}
	job, err := svc.Manager().Store().GetJob(ctx, jobID)
	if err != nil {
		return IntelJobItemPage{}, mapIntelError(err)
	}
	items, err := svc.Manager().Store().ListJobItems(ctx, store.JobItemFilter{
		JobID: jobID, Status: normalizeJobItemStatus(status), Limit: limit, Offset: offset,
	})
	if err != nil {
		return IntelJobItemPage{}, internal("list intel job items", err)
	}
	total, err := svc.Manager().Store().CountJobItems(ctx, jobID, normalizeJobItemStatus(status))
	if err != nil {
		return IntelJobItemPage{}, internal("count intel job items", err)
	}
	return IntelJobItemPage{Items: items, Total: total, JobID: jobID, JobState: job.Status}, nil
}

// CancelIntelJob cancels a job.
func (s *ControlPlaneService) CancelIntelJob(ctx context.Context, jobID string) (store.Job, error) {
	svc, serr := s.intelService()
	if serr != nil {
		return store.Job{}, serr
	}
	if err := svc.Manager().Cancel(ctx, jobID); err != nil {
		return store.Job{}, mapIntelError(err)
	}
	job, err := svc.Manager().Store().GetJob(ctx, jobID)
	if err != nil {
		return store.Job{}, mapIntelError(err)
	}
	return job, nil
}

// RetryFailedIntelJob requeues the failed items of a job.
func (s *ControlPlaneService) RetryFailedIntelJob(ctx context.Context, jobID string) (store.Job, int64, error) {
	svc, serr := s.intelService()
	if serr != nil {
		return store.Job{}, 0, serr
	}
	retried, err := svc.Manager().RetryFailed(ctx, jobID)
	if err != nil {
		return store.Job{}, 0, mapIntelError(err)
	}
	job, err := svc.Manager().Store().GetJob(ctx, jobID)
	if err != nil {
		return store.Job{}, 0, mapIntelError(err)
	}
	return job, retried, nil
}

// SubscribeIntelJob registers an SSE client for one job.
func (s *ControlPlaneService) SubscribeIntelJob(ctx context.Context, jobID string) (*jobs.Subscription, error) {
	svc, serr := s.intelService()
	if serr != nil {
		return nil, serr
	}
	if _, err := svc.Manager().Store().GetJob(ctx, jobID); err != nil {
		return nil, mapIntelError(err)
	}
	sub, err := svc.Manager().Subscribe(jobID)
	if err != nil {
		return nil, mapIntelError(err)
	}
	return sub, nil
}

// IntelStatusReport assembles GET /api/v1/intel/status.
func (s *ControlPlaneService) IntelStatusReport(ctx context.Context) (IntelStatus, error) {
	svc, serr := s.intelService()
	if serr != nil {
		return IntelStatus{}, serr
	}
	st := svc.Store()

	bytes, err := st.FileBytes()
	if err != nil {
		return IntelStatus{}, internal("intel.db size", err)
	}
	assessments, err := st.CountAssessments(ctx)
	if err != nil {
		return IntelStatus{}, internal("count assessments", err)
	}
	nodes, err := st.ListNodeHashes(ctx)
	if err != nil {
		return IntelStatus{}, internal("list intel nodes", err)
	}

	states, err := st.ListProviderStates(ctx)
	if err != nil {
		return IntelStatus{}, internal("list provider states", err)
	}
	providers := make([]IntelProviderStatus, 0, len(states))
	for _, state := range states {
		counts, err := st.ProviderQueueCounts(ctx, state.Provider)
		if err != nil {
			return IntelStatus{}, internal("provider queue counts", err)
		}
		providers = append(providers, IntelProviderStatus{
			Provider:        state.Provider,
			Day:             state.Day,
			Used:            state.Used,
			NextRequestAtNs: state.NextRequestAtNs,
			BlockedUntilNs:  state.BlockedUntilNs,
			Paused:          state.Paused,
			ErrorCode:       state.ErrorCode,
			HasCredential:   state.CredentialID != "",
			CredentialID:    state.CredentialID,
			Queued:          counts.Queued,
			Running:         counts.Running,
			Done:            counts.Done,
			Failed:          counts.Failed,
		})
	}

	stats := svc.Manager().Stats()
	jobsByStatus := make(map[string]int, 6)
	for _, status := range []string{
		store.JobQueued, store.JobRunning, store.JobSucceeded,
		store.JobPartial, store.JobFailed, store.JobCanceled,
	} {
		count, err := st.CountJobs(ctx, status)
		if err != nil {
			return IntelStatus{}, internal("count jobs", err)
		}
		jobsByStatus[status] = count
	}

	return IntelStatus{
		Enabled: svc.Manager().Enabled(),
		Database: IntelDatabaseStatus{
			FileBytes:   bytes,
			Assessments: assessments,
			NodeRecords: len(nodes),
		},
		Executor: stats,
		Discarded: IntelDiscarded{
			EgressObservations: svc.DiscardedEgressObservations(),
			SSEFrames:          stats.SSEDroppedFrames,
		},
		Providers:  providers,
		JobsByStat: jobsByStatus,
	}, nil
}

// IntelNodeReport assembles GET /api/v1/intel/nodes/{hash}.
func (s *ControlPlaneService) IntelNodeReport(ctx context.Context, nodeHash string) (IntelNodeDetail, error) {
	svc, serr := s.intelService()
	if serr != nil {
		return IntelNodeDetail{}, serr
	}
	st := svc.Store()

	detail := IntelNodeDetail{NodeHash: nodeHash}
	egress, ok, err := st.GetNodeEgress(ctx, nodeHash)
	if err != nil {
		return IntelNodeDetail{}, internal("read node egress", err)
	}
	if ok {
		detail.Egress = egress
	}
	if detail.History, err = st.ListEgressHistory(ctx, nodeHash, 20); err != nil {
		return IntelNodeDetail{}, internal("read egress history", err)
	}
	if detail.Checks, err = st.ListNodeChecks(ctx, nodeHash); err != nil {
		return IntelNodeDetail{}, internal("read node checks", err)
	}
	if detail.IPv4, err = s.intelAssessment(ctx, svc, egress.IPv4); err != nil {
		return IntelNodeDetail{}, err
	}
	if detail.IPv6, err = s.intelAssessment(ctx, svc, egress.IPv6); err != nil {
		return IntelNodeDetail{}, err
	}
	if detail.IPv4 != nil {
		lite := intel.AssessmentLiteFromRow(*detail.IPv4)
		detail.V4Projected = &lite
	}
	if detail.IPv6 != nil {
		lite := intel.AssessmentLiteFromRow(*detail.IPv6)
		detail.V6Projected = &lite
	}
	return detail, nil
}

// IntelIPReport assembles GET /api/v1/intel/ip/{ip}.
func (s *ControlPlaneService) IntelIPReport(ctx context.Context, ip string) (IntelIPDetail, error) {
	svc, serr := s.intelService()
	if serr != nil {
		return IntelIPDetail{}, serr
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return IntelIPDetail{}, invalidArg("ip: must be a valid address")
	}
	ip = addr.Unmap().String()
	st := svc.Store()

	detail := IntelIPDetail{IP: ip}
	if detail.Evidence, err = st.ListEvidenceByIP(ctx, ip); err != nil {
		return IntelIPDetail{}, internal("read evidence", err)
	}
	if detail.Assessment, err = s.intelAssessment(ctx, svc, ip); err != nil {
		return IntelIPDetail{}, err
	}
	if detail.Assessment != nil {
		lite := intel.AssessmentLiteFromRow(*detail.Assessment)
		detail.Projection = &lite
	}
	if detail.Nodes, err = st.ListNodesByIP(ctx, ip, maxIntelIPNodes); err != nil {
		return IntelIPDetail{}, internal("read nodes for ip", err)
	}
	return detail, nil
}

func (s *ControlPlaneService) intelAssessment(ctx context.Context, svc *intel.Service, ip string) (*store.Assessment, error) {
	if ip == "" {
		return nil, nil
	}
	row, ok, err := svc.Store().GetAssessment(ctx, ip)
	if err != nil {
		return nil, internal("read assessment", err)
	}
	if !ok {
		return nil, nil
	}
	return &row, nil
}

func normalizeJobStatus(status string) string {
	switch status {
	case store.JobQueued, store.JobRunning, store.JobSucceeded, store.JobPartial, store.JobFailed, store.JobCanceled:
		return status
	default:
		return ""
	}
}

func normalizeJobItemStatus(status string) string {
	switch status {
	case store.ItemQueued, store.ItemRunning, store.ItemDone, store.ItemFailed, store.ItemSkipped, store.ItemCanceled:
		return status
	default:
		return ""
	}
}

// mapIntelError converts the intel/jobs sentinels into service errors so the API
// layer can render the documented status codes (WP08 §3.1).
func mapIntelError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, jobs.ErrDisabled):
		return conflict("intel jobs are disabled (intel_enabled=false)")
	case errors.Is(err, jobs.ErrTooManyNodes):
		return invalidArg(err.Error())
	case errors.Is(err, jobs.ErrInvalidJob):
		return invalidArg(err.Error())
	case errors.Is(err, jobs.ErrTooManySubscribers):
		return &ServiceError{Code: "RATE_LIMITED", Message: err.Error(), Err: err}
	case errors.Is(err, store.ErrJobNotFound):
		return notFound("job not found")
	default:
		return internal("intel job", err)
	}
}
