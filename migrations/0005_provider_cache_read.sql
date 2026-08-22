-- Provider best cache-read price so market_cost can be computed exactly.
ALTER TABLE model_prices
  ADD COLUMN IF NOT EXISTS provider_cache_read_per_1m numeric(10,6) NOT NULL DEFAULT 0;
