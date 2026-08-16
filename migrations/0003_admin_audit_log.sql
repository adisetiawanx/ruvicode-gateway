CREATE TABLE IF NOT EXISTS admin_audit_log (
  id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  admin_email TEXT NOT NULL,
  action TEXT NOT NULL,
  details JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
