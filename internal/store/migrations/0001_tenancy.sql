-- 0001_tenancy.sql — tenants, users, memberships, sessions.
--
-- Tenancy is the highest-severity requirement in the product, so it is modelled
-- first and enforced at the database, not only in the application. See
-- mvp-design.md §3 (data model) and §10 (RLS).

-- ---------------------------------------------------------------------------
-- tenants
-- ---------------------------------------------------------------------------
CREATE TABLE tenants (
    id          TEXT PRIMARY KEY,
    name        TEXT        NOT NULL,
    slug        TEXT        NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- users (global identity; a person may belong to several tenants)
-- ---------------------------------------------------------------------------
CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    email         TEXT        NOT NULL UNIQUE,
    password_hash TEXT        NOT NULL,
    name          TEXT        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- tenant_memberships — teams live INSIDE a tenant, never across one.
-- ---------------------------------------------------------------------------
CREATE TABLE tenant_memberships (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    user_id    TEXT        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role       TEXT        NOT NULL CHECK (role IN ('owner', 'admin', 'member', 'guest')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, user_id)
);

CREATE INDEX tenant_memberships_user_idx ON tenant_memberships (user_id);

-- ---------------------------------------------------------------------------
-- sessions — carries the active tenant so tenancy resolves on every request.
-- ---------------------------------------------------------------------------
CREATE TABLE sessions (
    id         TEXT PRIMARY KEY,
    user_id    TEXT        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    tenant_id  TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    token_hash TEXT        NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX sessions_expiry_idx ON sessions (expires_at);

-- ---------------------------------------------------------------------------
-- Row-level security.
--
-- FORCE is required: without it the table owner bypasses every policy, and the
-- application must connect as a role that does NOT own these tables. That is
-- the whole point — a forgotten `WHERE tenant_id = $1` in the repository layer
-- becomes an error rather than a cross-tenant leak (NFR / FR-TN-1).
--
-- Tenants and tenant_memberships are read *before* a scope exists (during
-- resolution), so they are resolved with the owner role; everything else is
-- scoped.
-- ---------------------------------------------------------------------------

-- The app role cannot bypass RLS and is not the owner.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aic_app') THEN
        CREATE ROLE aic_app NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
    END IF;
END
$$;

GRANT USAGE ON SCHEMA public TO aic_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO aic_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO aic_app;

-- users is global (not tenant-scoped) but holds password hashes, so the app role
-- gets column-level access to the display fields only. Revoking the blanket grant
-- first is required, or the column grants below would be additive.
REVOKE ALL ON users FROM aic_app;
GRANT SELECT (id, email, name, created_at) ON users TO aic_app;

-- Helper: the current transaction's tenant. Returns NULL when unset, which
-- makes every scoped policy fail closed (no rows visible) rather than open.
CREATE OR REPLACE FUNCTION current_tenant_id() RETURNS TEXT
    LANGUAGE sql STABLE AS $$
    SELECT NULLIF(current_setting('app.tenant_id', true), '')
$$;

-- RLS on the identity plane.
--
-- Deliberately ENABLE and not FORCE. Session and membership resolution happen
-- *before* a tenant is known (login, tenant picker), so those queries must run
-- on the owner connection. FORCE makes the owner subject to the policy too,
-- which would return zero rows for the very query that establishes which
-- tenant to scope to -- i.e. login could never succeed.
--
-- The guarantee is therefore structural rather than policy-based, and §10 says
-- so explicitly:
--   * owner pool -> identity plane only (tenants, users, memberships, sessions)
--   * app pool   -> everything else, connected as aic_app, which owns nothing
--                   and lacks BYPASSRLS, so policies always apply to it.
-- Every *content* table added later is ENABLE + FORCE, because owner-pool
-- access to content is a deliberate admin action, not a request path.
ALTER TABLE tenant_memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE sessions         ENABLE ROW LEVEL SECURITY;

CREATE POLICY memberships_tenant_isolation ON tenant_memberships
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());

CREATE POLICY sessions_tenant_isolation ON sessions
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());