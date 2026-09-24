// WP08 §3/§7 client types for the batch job surface.
//
// Server DTOs: internal/intel/store/models.go (Job, JobItem, JobProgress),
// internal/api/handler_intel.go (page envelopes at line 280, SSE frames at
// line 256) and internal/service/control_plane_intel.go (IntelJobDetail).

export type JobKind = "egress" | "intel" | "checks" | "full";

export type JobStatus = "queued" | "running" | "succeeded" | "partial" | "failed" | "canceled";

export type JobItemStatus = "queued" | "running" | "done" | "failed" | "skipped" | "canceled";

// store.Job — one row of the jobs table.
export type IntelJob = {
  id: string;
  kind: string;
  status: string;
  priority: number;
  request_json: string;
  total: number;
  done: number;
  failed: number;
  skipped: number;
  created_by: string;
  created_at_ns: number;
  started_at_ns: number;
  finished_at_ns: number;
  error: string;
};

// store.JobProgress — the aggregated counter view of one job.
export type IntelJobProgress = {
  job_id: string;
  status: string;
  total: number;
  done: number;
  failed: number;
  skipped: number;
  canceled: number;
  pending: number;
  pending_online_lookups: number;
};

// service.IntelJobDetail — GET /api/v1/intel/jobs/{id}.
export type IntelJobDetail = {
  job: IntelJob;
  progress: IntelJobProgress;
};

// store.JobItem — the per-node progress of one job.
export type IntelJobItem = {
  job_id: string;
  node_hash: string;
  status: string;
  step_index: number;
  attempts: number;
  next_run_at_ns: number;
  result_json: string;
  error_code: string;
  updated_at_ns: number;
};

// writeIntelPage envelope (handler_intel.go:280).
export type PageEnvelope<T> = {
  items: T[];
  total: number;
  limit: number;
  offset: number;
};

export type IntelJobPage = PageEnvelope<IntelJob>;
export type IntelJobItemPage = PageEnvelope<IntelJobItem>;

// jobs.Request — POST /api/v1/intel/jobs (internal/intel/jobs/jobs.go:149).
// The endpoint decodes with DisallowUnknownFields, so the body is limited to
// exactly these keys; the job priority is not part of the contract and is
// fixed server side (control_plane_intel.go:113 uses PriorityManual = 100).
export type IntelJobScope = {
  all?: boolean;
  node_hashes?: string[];
};

export type CreateIntelJobRequest = {
  kind: JobKind;
  scope: IntelJobScope;
  force?: boolean;
};

export type CreateIntelJobResponse = {
  job: IntelJob;
};

export type CancelIntelJobResponse = {
  job: IntelJob;
};

export type RetryFailedJobResponse = {
  job: IntelJob;
  retried: number;
};

// SSE payload of a `progress` frame (internal/intel/jobs/hub.go:11). The first
// frame the handler replays only carries {"status":"unknown"} when the job row
// is not readable yet, so every counter is optional.
export type JobProgressFrame = {
  done?: number;
  failed?: number;
  skipped?: number;
  total?: number;
  status?: string;
};
