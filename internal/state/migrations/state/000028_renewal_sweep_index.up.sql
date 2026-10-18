-- Index the renewal sweep.
--
-- ListRenewableSubscriptions runs once a minute and asks for the subscriptions
-- that renew themselves and expire inside the lead time:
--
--   WHERE status = 'active' AND auto_renew = 1 AND expires_at_ns > 0
--     AND expires_at_ns <= ?
--
-- Nothing indexed auto_renew or expires_at_ns. ux_user_subscriptions_active is
-- partial on status = 'active' and keys on user_id, so it cannot answer a range
-- on expires_at_ns, and the sweep scanned every subscription row - retired ones
-- included - on every pass.
--
-- The index is partial on the same predicate the sweep uses, so it holds only
-- the rows that can ever renew themselves: a subscription with auto_renew = 0
-- is not in it, and neither is a retired one. That keeps it small and makes the
-- sweep a range scan over the renewals that are actually due.
CREATE INDEX IF NOT EXISTS idx_user_subscriptions_renewal
    ON user_subscriptions(expires_at_ns)
    WHERE status = 'active' AND auto_renew = 1;
