-- ADR-032 supplement: market_cost column on usage_records.
-- The reported upstream_inference_cost is wholesale infra cost (not what
-- the wallet is charged). market_cost is the estimated real wallet charge,
-- computed at finalize from best_input/output/cache_read prices.
ALTER TABLE usage_records
  ADD COLUMN IF NOT EXISTS market_cost decimal(12,8) NOT NULL DEFAULT 0;
