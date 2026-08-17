CREATE TABLE IF NOT EXISTS admin_audit_log (
  id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  admin_email TEXT NOT NULL,
  action TEXT NOT NULL,
  operation_id TEXT,
  status TEXT NOT NULL DEFAULT 'completed',
  details JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE admin_audit_log ADD COLUMN IF NOT EXISTS operation_id TEXT;
ALTER TABLE admin_audit_log ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'completed';
ALTER TABLE admin_audit_log ALTER COLUMN details TYPE JSONB USING CASE WHEN details IS NULL THEN '{}'::jsonb ELSE details::jsonb END;
ALTER TABLE admin_audit_log ALTER COLUMN details SET DEFAULT '{}'::jsonb;
CREATE INDEX IF NOT EXISTS idx_admin_audit_created ON admin_audit_log (created_at DESC);
