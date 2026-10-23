-- 0012_integrations.sql — tenant-scoped references to externally held secrets.
CREATE TABLE integration_credentials (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('github', 'jira', 'anthropic', 'openai')),
    secret_ref TEXT NOT NULL,
    label      TEXT NOT NULL DEFAULT '',
    created_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, kind),
    UNIQUE (id, tenant_id)
);

COMMENT ON TABLE integration_credentials IS
    'Stores a reference to a secret held elsewhere (the OS keychain), never the secret itself.';
COMMENT ON COLUMN integration_credentials.secret_ref IS
    'Reference to an OS keychain secret; never contains the secret value.';

ALTER TABLE integration_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE integration_credentials FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON integration_credentials
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON integration_credentials TO aic_app;
