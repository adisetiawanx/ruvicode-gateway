-- ADR-032: Cached input token billing
-- Two new columns on model_prices for cache read pricing (ref + user).
-- Reconstructed from best_cache_read / (1 - provider_discount) at sync time.
ALTER TABLE model_prices
  ADD COLUMN IF NOT EXISTS ref_cache_read_per_1m numeric(10,6) NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS user_cache_read_per_1m numeric(10,6) NOT NULL DEFAULT 0;

-- usage_records: cache_read_tokens nullable (null = historical, 0 = measured none).
ALTER TABLE usage_records
  ADD COLUMN IF NOT EXISTS cache_read_tokens integer;
