CREATE TABLE usage_reservations (
 id TEXT PRIMARY KEY, key_id TEXT NOT NULL, user_id TEXT NOT NULL,
 platform_id TEXT NOT NULL, subscription_id TEXT NOT NULL,
 period_start_ns INTEGER NOT NULL, hour_ns INTEGER NOT NULL,
 bytes INTEGER NOT NULL CHECK(bytes > 0), ingress INTEGER NOT NULL CHECK(ingress IN (0,1))
);
CREATE INDEX usage_reservations_user ON usage_reservations(user_id);
