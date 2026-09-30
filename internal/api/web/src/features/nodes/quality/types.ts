export type QualityState = "unobserved" | "pending" | "partial" | "valid" | "stale" | "conflicting" | "unsupported";
export type RiskGrade = "unknown" | "low" | "moderate" | "high" | "severe";
export type IPType = "unknown" | "residential" | "non_residential" | "datacenter" | "mobile" | "business" | "wireless" | "conflicting";
export type QualitySignals = Record<"proxy" | "vpn" | "tor" | "hosting" | "compromised" | "scraper" | "anonymous", boolean | null>;

export type QualityEvidence = {
  id: string;
  ip: string;
  provider: string;
  profile: string;
  ip_type: IPType;
  source_type: string;
  asn: string;
  organization: string;
  country_code: string;
  network_provider?: string;
  operator?: string;
  operator_services?: string[];
  attack_history?: Record<string, number>;
  native?: boolean;
  tor_roles?: Array<"exit" | "guard" | "relay">;
  risk_score: number | null;
  grade: RiskGrade;
  source_confidence: number | null;
  signals: QualitySignals;
  observed_at: string;
  valid_until: string;
  source_updated_at?: string;
  abuse_confidence?: number;
  total_reports?: number;
  distinct_reporters?: number;
  last_reported_at?: string;
  report_window_days?: number;
};

export type InspectionTask = {
  ip: string;
  provider: string;
  profile: string;
  state: "queued" | "running" | "done" | "failed";
  generation: number;
  attempt: number;
  requested_at: string;
  next_run_at: string;
  last_attempt_at: string;
  error_code?: string;
  error_message?: string;
};

export type QualitySource = {
  provider: string;
  configured: boolean;
  state: QualityState;
  evidence: QualityEvidence | null;
  task?: InspectionTask;
};

export type QualitySummary = {
  assessment?: QualityAssessment;
  ip?: string;
  state: QualityState;
  evidence: QualityEvidence | null;
  task?: InspectionTask;
  sources: QualitySource[];
};

export type QualityAssessment = {
  state: QualityState;
  score_source: "ippure";
  purity_score: number | null;
  purity_band: string;
  network_type: IPType;
  network_source: string;
  native: boolean | null;
  verdict: "pending" | "incomplete" | "review" | "conflicting" | "caution" | "high_risk" | "favorable";
  // Nullable because the projection path emitted `null` for an empty list (Go's
  // nil slice). The server now sends [], but the type stays honest so a
  // regression becomes a type error instead of a blank page.
  reasons: string[] | null;
  tor_roles?: Array<"exit" | "guard" | "relay">;
};

export type SourceStatus = {
  id: string;
  name: string;
  website: string;
  configured: boolean;
  requires_key: boolean;
  has_key: boolean;
  daily_limit: number;
  used_today: number;
  queued: number;
  running: number;
  failed: number;
  paused: boolean;
  next_allowed_at?: string;
  error_code?: string;
};

export type QualityStatus = {
  enabled: boolean;
  known_ips: number;
  checked_ips: number;
  low_risk_ips: number;
  high_risk_ips: number;
  stale_ips: number;
  queue_capacity: number;
  dropped_observations: number;
  storage_error?: string;
  sources: SourceStatus[];
  manual_sources?: ManualSourceStatus[];
  registry_sources?: Array<{ id: string; ready: boolean; entries: number; updated_at?: string; error_code?: string }>;
};

export type ManualSourceStatus = {
  id: string;
  name: string;
  website: string;
  busy: boolean;
  interval_seconds: number;
  current_ips: number;
  next_allowed_at?: string;
};

export type IPPureReview = {
  evidence: QualityEvidence | null;
  node_hash: string;
  expected_ip: string;
  matches_node_ip: boolean;
  is_residential: boolean | null;
  score_supported: boolean;
  next_allowed_at: string;
  // WP08 §9: the review runs as an intel job and waits up to 15 seconds for the
  // evidence. When it is not ready yet the response is 202 with queued=true and
  // job_id set, and the caller polls /api/v1/intel/jobs/{job_id}.
  job_id?: string;
  queued: boolean;
};

export type InspectionResult = { quality: QualitySummary; queued: boolean; warnings?: string[]; job_id?: string };
