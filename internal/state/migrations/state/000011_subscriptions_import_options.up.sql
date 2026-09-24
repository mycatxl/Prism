ALTER TABLE subscriptions ADD COLUMN auto_intel INTEGER NOT NULL DEFAULT 1;
ALTER TABLE subscriptions ADD COLUMN user_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE subscriptions ADD COLUMN last_parse_report_json TEXT NOT NULL DEFAULT '{}';
