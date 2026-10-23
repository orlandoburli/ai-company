-- 0021_engine_runners.sql — the tenant's default variables.
--
-- The engine resolves the runner for a node from the run's variables, and the
-- run's variables come from two places: what the run was started with, and
-- what the tenant keeps as a default. The defaults live here, so an operator
-- can say "every run of this company uses the `claude` runner" once.
--
-- A variable is a NAME and a string value. The name is a plain identifier
-- ([A-Za-z_][A-Za-z0-9_]{0,63}) because it is referenced from a template's
-- config as `$NAME`, and the value is a string because a variable names a
-- runner, a branch, a path — a value, not a document.
--
-- The table is tenant-scoped like every other table in this schema: ENABLE +
-- FORCE ROW LEVEL SECURITY and the tenant_isolation policy, so a forgotten
-- tenant predicate is an empty result rather than another company's defaults.

CREATE TABLE tenant_variables (
    tenant_id  TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name       TEXT        NOT NULL
                           CHECK (name ~ '^[A-Za-z_][A-Za-z0-9_]{0,63}$'),
    value      TEXT        NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, name)
);

COMMENT ON TABLE tenant_variables IS
    'The tenant''s default variables: the values a run inherits when it does not set them itself.';
COMMENT ON COLUMN tenant_variables.name IS
    'The variable name; referenced from a node config as $NAME.';

-- ---------------------------------------------------------------------------
-- Row-level security: ENABLE + FORCE, exactly like every other tenant table.
-- ---------------------------------------------------------------------------
ALTER TABLE tenant_variables ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_variables FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON tenant_variables
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());

-- ---------------------------------------------------------------------------
-- Privileges, spelled out so a later change to the defaults cannot silently
-- change this file's contract.
-- ---------------------------------------------------------------------------
GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_variables TO aic_app;
