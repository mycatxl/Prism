CREATE TABLE privacy_deleted_hashes (node_hash TEXT PRIMARY KEY);
CREATE TRIGGER privacy_fence_node_egress_insert BEFORE INSERT ON node_egress WHEN EXISTS (SELECT 1 FROM privacy_deleted_hashes WHERE node_hash=NEW.node_hash) BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER privacy_fence_node_egress_update BEFORE UPDATE ON node_egress WHEN EXISTS (SELECT 1 FROM privacy_deleted_hashes WHERE node_hash=NEW.node_hash) BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER privacy_fence_egress_history_insert BEFORE INSERT ON egress_history WHEN EXISTS (SELECT 1 FROM privacy_deleted_hashes WHERE node_hash=NEW.node_hash) BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER privacy_fence_egress_history_update BEFORE UPDATE ON egress_history WHEN EXISTS (SELECT 1 FROM privacy_deleted_hashes WHERE node_hash=NEW.node_hash) BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER privacy_fence_node_checks_insert BEFORE INSERT ON node_checks WHEN EXISTS (SELECT 1 FROM privacy_deleted_hashes WHERE node_hash=NEW.node_hash) BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER privacy_fence_node_checks_update BEFORE UPDATE ON node_checks WHEN EXISTS (SELECT 1 FROM privacy_deleted_hashes WHERE node_hash=NEW.node_hash) BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER privacy_fence_job_items_insert BEFORE INSERT ON job_items WHEN EXISTS (SELECT 1 FROM privacy_deleted_hashes WHERE node_hash=NEW.node_hash) BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER privacy_fence_job_items_update BEFORE UPDATE ON job_items WHEN EXISTS (SELECT 1 FROM privacy_deleted_hashes WHERE node_hash=NEW.node_hash) BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER privacy_fence_provider_node_state_insert BEFORE INSERT ON provider_node_state WHEN EXISTS (SELECT 1 FROM privacy_deleted_hashes WHERE node_hash=NEW.node_hash) BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER privacy_fence_provider_node_state_update BEFORE UPDATE ON provider_node_state WHEN EXISTS (SELECT 1 FROM privacy_deleted_hashes WHERE node_hash=NEW.node_hash) BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER privacy_fence_evidence_insert BEFORE INSERT ON evidence WHEN EXISTS (SELECT 1 FROM privacy_deleted_hashes WHERE node_hash=NEW.via_node_hash) BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER privacy_fence_evidence_update BEFORE UPDATE ON evidence WHEN EXISTS (SELECT 1 FROM privacy_deleted_hashes WHERE node_hash=NEW.via_node_hash) BEGIN SELECT RAISE(IGNORE); END;
CREATE TABLE privacy_deleted_ips (ip TEXT PRIMARY KEY);
CREATE TRIGGER privacy_ip_fence_ip_assessment_insert BEFORE INSERT ON ip_assessment WHEN EXISTS (SELECT 1 FROM privacy_deleted_ips WHERE ip=NEW.ip) AND NOT EXISTS (SELECT 1 FROM node_egress WHERE ipv4=NEW.ip OR ipv6=NEW.ip) BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER privacy_ip_fence_ip_assessment_update BEFORE UPDATE ON ip_assessment WHEN EXISTS (SELECT 1 FROM privacy_deleted_ips WHERE ip=NEW.ip) AND NOT EXISTS (SELECT 1 FROM node_egress WHERE ipv4=NEW.ip OR ipv6=NEW.ip) BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER privacy_ip_fence_evidence_insert BEFORE INSERT ON evidence WHEN EXISTS (SELECT 1 FROM privacy_deleted_ips WHERE ip=NEW.ip) AND NOT EXISTS (SELECT 1 FROM node_egress WHERE ipv4=NEW.ip OR ipv6=NEW.ip) BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER privacy_ip_fence_evidence_update BEFORE UPDATE ON evidence WHEN EXISTS (SELECT 1 FROM privacy_deleted_ips WHERE ip=NEW.ip) AND NOT EXISTS (SELECT 1 FROM node_egress WHERE ipv4=NEW.ip OR ipv6=NEW.ip) BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER privacy_ip_fence_provider_queue_insert BEFORE INSERT ON provider_queue WHEN EXISTS (SELECT 1 FROM privacy_deleted_ips WHERE ip=NEW.ip) AND NOT EXISTS (SELECT 1 FROM node_egress WHERE ipv4=NEW.ip OR ipv6=NEW.ip) BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER privacy_ip_fence_provider_queue_update BEFORE UPDATE ON provider_queue WHEN EXISTS (SELECT 1 FROM privacy_deleted_ips WHERE ip=NEW.ip) AND NOT EXISTS (SELECT 1 FROM node_egress WHERE ipv4=NEW.ip OR ipv6=NEW.ip) BEGIN SELECT RAISE(IGNORE); END;
-- Per-node terminal measurement state survives job-item log retention.
CREATE TABLE node_measurements (
 node_hash TEXT PRIMARY KEY, job_id TEXT NOT NULL, status TEXT NOT NULL,
 step_index INTEGER NOT NULL, attempts INTEGER NOT NULL, error_code TEXT NOT NULL,
 updated_at_ns INTEGER NOT NULL, full_attempted INTEGER NOT NULL DEFAULT 0
);
INSERT INTO node_measurements
SELECT node_hash,job_id,status,step_index,attempts,error_code,updated_at_ns,
 EXISTS(SELECT 1 FROM job_items f JOIN jobs j ON j.id=f.job_id WHERE f.node_hash=latest.node_hash AND j.kind='full')
FROM (SELECT *,ROW_NUMBER() OVER(PARTITION BY node_hash ORDER BY updated_at_ns DESC,job_id DESC) AS rn FROM job_items) latest WHERE rn=1;
CREATE TRIGGER privacy_fence_node_measurements_insert BEFORE INSERT ON node_measurements
WHEN EXISTS(SELECT 1 FROM privacy_deleted_hashes WHERE node_hash=NEW.node_hash) BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER privacy_fence_node_measurements_update BEFORE UPDATE ON node_measurements
WHEN EXISTS(SELECT 1 FROM privacy_deleted_hashes WHERE node_hash=NEW.node_hash) BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER node_measurement_scheduled AFTER INSERT ON jobs WHEN NEW.kind='full' BEGIN
 INSERT INTO node_measurements(node_hash,job_id,status,step_index,attempts,error_code,updated_at_ns,full_attempted)
 SELECT value,NEW.id,'queued',0,0,'',NEW.created_at_ns,1 FROM json_each(NEW.request_json,'$.scope.node_hashes') WHERE true
 ON CONFLICT(node_hash) DO UPDATE SET job_id=excluded.job_id,status='queued',attempts=0,error_code='',updated_at_ns=excluded.updated_at_ns,full_attempted=1;
END;
CREATE TRIGGER node_measurement_job_terminal AFTER UPDATE OF status ON jobs WHEN NEW.status IN ('canceled','failed','partial','succeeded') BEGIN
 UPDATE node_measurements SET status=CASE WHEN NEW.status='canceled' THEN 'canceled' ELSE 'failed' END,
 error_code=CASE WHEN NEW.status='canceled' THEN 'JOB_CANCELED' ELSE 'JOB_ENDED_BEFORE_MEASUREMENT' END,
 updated_at_ns=MAX(updated_at_ns,NEW.finished_at_ns)
 WHERE job_id=NEW.id AND status IN ('queued','running');
END;
CREATE TRIGGER node_measurement_insert AFTER INSERT ON job_items BEGIN
 INSERT INTO node_measurements VALUES(NEW.node_hash,NEW.job_id,NEW.status,NEW.step_index,NEW.attempts,NEW.error_code,NEW.updated_at_ns,
 EXISTS(SELECT 1 FROM jobs WHERE id=NEW.job_id AND kind='full'))
 ON CONFLICT(node_hash) DO UPDATE SET job_id=excluded.job_id,status=excluded.status,step_index=excluded.step_index,attempts=excluded.attempts,error_code=excluded.error_code,updated_at_ns=excluded.updated_at_ns,full_attempted=MAX(node_measurements.full_attempted,excluded.full_attempted)
 WHERE excluded.updated_at_ns>=node_measurements.updated_at_ns;
END;
CREATE TRIGGER node_measurement_update AFTER UPDATE ON job_items BEGIN
 INSERT INTO node_measurements VALUES(NEW.node_hash,NEW.job_id,NEW.status,NEW.step_index,NEW.attempts,NEW.error_code,NEW.updated_at_ns,
 EXISTS(SELECT 1 FROM jobs WHERE id=NEW.job_id AND kind='full'))
 ON CONFLICT(node_hash) DO UPDATE SET job_id=excluded.job_id,status=excluded.status,step_index=excluded.step_index,attempts=excluded.attempts,error_code=excluded.error_code,updated_at_ns=excluded.updated_at_ns,full_attempted=MAX(node_measurements.full_attempted,excluded.full_attempted)
 WHERE excluded.updated_at_ns>=node_measurements.updated_at_ns;
END;
CREATE TRIGGER privacy_job_scope_insert AFTER INSERT ON jobs
 WHEN EXISTS(SELECT 1 FROM json_each(NEW.request_json,'$.scope.node_hashes') j JOIN privacy_deleted_hashes d ON d.node_hash=j.value)
 BEGIN
 UPDATE jobs SET request_json=json_set(NEW.request_json,'$.scope.node_hashes',json((SELECT json_group_array(value) FROM json_each(NEW.request_json,'$.scope.node_hashes') WHERE value NOT IN(SELECT node_hash FROM privacy_deleted_hashes)))) WHERE id=NEW.id;
 END;
CREATE TRIGGER privacy_job_scope_update AFTER UPDATE OF request_json ON jobs
 WHEN EXISTS(SELECT 1 FROM json_each(NEW.request_json,'$.scope.node_hashes') j JOIN privacy_deleted_hashes d ON d.node_hash=j.value)
 BEGIN
 UPDATE jobs SET request_json=json_set(NEW.request_json,'$.scope.node_hashes',json((SELECT json_group_array(value) FROM json_each(NEW.request_json,'$.scope.node_hashes') WHERE value NOT IN(SELECT node_hash FROM privacy_deleted_hashes)))) WHERE id=NEW.id;
 END;
