-- Plan pricing and subscription renewal.
--
-- price_micro is what a plan costs per period, in micro-units of USDT. Zero is
-- not "free": it means the plan is not for sale, so the console lists it to
-- operators only and no self-service purchase can name it. A free plan is
-- granted by an operator, not bought.
--
-- auto_renew and period_index live on the subscription, never in the plan
-- snapshot. The snapshot answers "what did this customer buy", and rewriting it
-- (which an operator's apply-to-existing does) must not silently flip somebody's
-- decision to renew. period_index counts periods elapsed and is the idempotency
-- key for a renewal: (subscription_id, period_index) is settled once.
--
-- Existing rows get price_micro = 0 and auto_renew = 0, which is exactly the
-- state they were in: nothing was for sale and nothing renewed by itself.
ALTER TABLE plans ADD COLUMN price_micro INTEGER NOT NULL DEFAULT 0;
ALTER TABLE user_subscriptions ADD COLUMN auto_renew INTEGER NOT NULL DEFAULT 0;
ALTER TABLE user_subscriptions ADD COLUMN period_index INTEGER NOT NULL DEFAULT 0;
