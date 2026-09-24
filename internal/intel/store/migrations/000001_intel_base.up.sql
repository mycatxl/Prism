-- WP08 §2: intel.db base schema.
--
-- intel.db is the authoritative store for egress observations, provider
-- evidence, via-node checks, purity assessments, provider budgets/queues and
-- batch jobs. Every *_ns column is a UTC Unix nanosecond timestamp (R8).

CREATE TABLE node_egress (
    node_hash      TEXT PRIMARY KEY,
    ipv4           TEXT NOT NULL DEFAULT '',
    ipv6           TEXT NOT NULL DEFAULT '',
    colo           TEXT NOT NULL DEFAULT '',
    loc            TEXT NOT NULL DEFAULT '',
    v4_observed_ns INTEGER NOT NULL DEFAULT 0,
    v6_observed_ns INTEGER NOT NULL DEFAULT 0,
    v6_checked_ns  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_node_egress_v4 ON node_egress(ipv4);
CREATE INDEX idx_node_egress_v6 ON node_egress(ipv6);

CREATE TABLE egress_history (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    node_hash      TEXT NOT NULL,
    family         INTEGER NOT NULL,
    ip             TEXT NOT NULL,
    observed_at_ns INTEGER NOT NULL
);
CREATE INDEX idx_egress_history_node ON egress_history(node_hash, observed_at_ns DESC);

CREATE TABLE evidence (
    ip              TEXT NOT NULL,
    provider        TEXT NOT NULL,
    profile         TEXT NOT NULL,
    via_node_hash   TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL,
    observed_at_ns  INTEGER NOT NULL,
    valid_until_ns  INTEGER NOT NULL,
    normalized_json TEXT NOT NULL,
    raw_json        TEXT,
    error_code      TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (ip, provider)
);
CREATE INDEX idx_evidence_valid ON evidence(valid_until_ns);
CREATE INDEX idx_evidence_via_node ON evidence(via_node_hash);

CREATE TABLE node_checks (
    node_hash      TEXT NOT NULL,
    check_id       TEXT NOT NULL,
    check_version  INTEGER NOT NULL,
    egress_ip      TEXT NOT NULL,
    outcome        TEXT NOT NULL,
    region         TEXT NOT NULL DEFAULT '',
    detail_json    TEXT NOT NULL DEFAULT '{}',
    latency_ms     INTEGER NOT NULL DEFAULT 0,
    observed_at_ns INTEGER NOT NULL,
    valid_until_ns INTEGER NOT NULL,
    PRIMARY KEY (node_hash, check_id)
);
CREATE INDEX idx_node_checks_ip ON node_checks(egress_ip);
CREATE INDEX idx_node_checks_valid ON node_checks(valid_until_ns);

CREATE TABLE ip_assessment (
    ip              TEXT PRIMARY KEY,
    profile         TEXT NOT NULL,
    state           TEXT NOT NULL,
    verdict         TEXT NOT NULL,
    purity_score    INTEGER,
    purity_band     TEXT NOT NULL,
    confidence      TEXT NOT NULL,
    coverage        REAL NOT NULL,
    ip_type         TEXT NOT NULL,
    native          INTEGER,
    flags           INTEGER NOT NULL DEFAULT 0,
    asn             INTEGER,
    as_org          TEXT NOT NULL DEFAULT '',
    country         TEXT NOT NULL DEFAULT '',
    city            TEXT NOT NULL DEFAULT '',
    reasons_json    TEXT NOT NULL,
    components_json TEXT NOT NULL,
    computed_at_ns  INTEGER NOT NULL,
    valid_until_ns  INTEGER NOT NULL
);
CREATE INDEX idx_ip_assessment_valid ON ip_assessment(valid_until_ns);
CREATE INDEX idx_ip_assessment_profile ON ip_assessment(profile);

CREATE TABLE provider_state (
    provider           TEXT PRIMARY KEY,
    day                TEXT NOT NULL,
    used               INTEGER NOT NULL DEFAULT 0,
    next_request_at_ns INTEGER NOT NULL DEFAULT 0,
    blocked_until_ns   INTEGER NOT NULL DEFAULT 0,
    paused             INTEGER NOT NULL DEFAULT 0,
    error_code         TEXT NOT NULL DEFAULT '',
    credential_id      TEXT NOT NULL DEFAULT ''
);

CREATE TABLE provider_queue (
    provider       TEXT NOT NULL,
    ip             TEXT NOT NULL,
    priority       INTEGER NOT NULL,
    job_id         TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL,
    attempts       INTEGER NOT NULL DEFAULT 0,
    next_run_at_ns INTEGER NOT NULL,
    lease_owner    TEXT NOT NULL DEFAULT '',
    lease_until_ns INTEGER NOT NULL DEFAULT 0,
    error_code     TEXT NOT NULL DEFAULT '',
    enqueued_at_ns INTEGER NOT NULL,
    PRIMARY KEY (provider, ip)
);
CREATE INDEX idx_provider_queue_claim ON provider_queue(provider, status, next_run_at_ns, priority DESC);
CREATE INDEX idx_provider_queue_job ON provider_queue(job_id);

CREATE TABLE jobs (
    id             TEXT PRIMARY KEY,
    kind           TEXT NOT NULL,
    status         TEXT NOT NULL,
    priority       INTEGER NOT NULL,
    request_json   TEXT NOT NULL,
    total          INTEGER NOT NULL DEFAULT 0,
    done           INTEGER NOT NULL DEFAULT 0,
    failed         INTEGER NOT NULL DEFAULT 0,
    skipped        INTEGER NOT NULL DEFAULT 0,
    created_by     TEXT NOT NULL,
    created_at_ns  INTEGER NOT NULL,
    started_at_ns  INTEGER NOT NULL DEFAULT 0,
    finished_at_ns INTEGER NOT NULL DEFAULT 0,
    error          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_jobs_status ON jobs(status, priority DESC, created_at_ns);

CREATE TABLE job_items (
    job_id         TEXT NOT NULL,
    node_hash      TEXT NOT NULL,
    status         TEXT NOT NULL,
    step_index     INTEGER NOT NULL DEFAULT 0,
    attempts       INTEGER NOT NULL DEFAULT 0,
    next_run_at_ns INTEGER NOT NULL,
    lease_owner    TEXT NOT NULL DEFAULT '',
    lease_until_ns INTEGER NOT NULL DEFAULT 0,
    result_json    TEXT NOT NULL DEFAULT '{}',
    error_code     TEXT NOT NULL DEFAULT '',
    updated_at_ns  INTEGER NOT NULL,
    PRIMARY KEY (job_id, node_hash)
);
CREATE INDEX idx_job_items_claim ON job_items(status, next_run_at_ns);
CREATE INDEX idx_job_items_job ON job_items(job_id, status);
