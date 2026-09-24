ALTER TABLE platforms ADD COLUMN quality_policy_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE platforms ADD COLUMN scheduled_rotation_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE platforms ADD COLUMN scheduled_rotation_interval_ns INTEGER NOT NULL DEFAULT 0;
ALTER TABLE platforms ADD COLUMN rotation_avoid_previous_ip INTEGER NOT NULL DEFAULT 1;
