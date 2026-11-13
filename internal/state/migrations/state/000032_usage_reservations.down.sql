-- Unsettled liability must never disappear during downgrade.
CREATE TEMP TABLE usage_downgrade_guard (n INTEGER CHECK(n = 0));
INSERT INTO usage_downgrade_guard SELECT count(*) FROM usage_reservations;
DROP TABLE usage_downgrade_guard;
DROP TABLE usage_reservations;
