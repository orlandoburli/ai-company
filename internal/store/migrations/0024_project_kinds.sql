-- 0024_project_kinds.sql — the kinds a project offers.
--
-- items.kind is free text by design (0004): there is deliberately no enum,
-- because users name their own kinds. What was missing was the list the
-- interface offers — the vocabulary a project presents when somebody creates an
-- item. This table holds that list, per project, in the order the project wants
-- it.
--
-- The list is an OFFER, not a constraint. An item may keep a kind its project no
-- longer lists, and a project may list kinds it does not use yet. Dropping a
-- kind from the list never rewrites an item and never invalidates one; the
-- items table is deliberately left untouched here.
--
-- The table is tenant-scoped like every other table in this schema: ENABLE +
-- FORCE ROW LEVEL SECURITY and the tenant_isolation policy, so a forgotten
-- tenant predicate is an empty result rather than another company's vocabulary.
-- The project reference is composite, so a kind can never point at a project in
-- another tenant.

CREATE TABLE project_kinds (
    id         TEXT        PRIMARY KEY,
    tenant_id  TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    project_id TEXT        NOT NULL,
    name       TEXT        NOT NULL
                           -- A kind is a name somebody types: it must say
                           -- something, and it must be short enough to sit in a
                           -- dropdown.
                           CHECK (length(btrim(name)) BETWEEN 1 AND 40),
    position   INT         NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    FOREIGN KEY (project_id, tenant_id) REFERENCES projects (id, tenant_id) ON DELETE CASCADE,

    -- A project cannot offer the same kind twice.
    UNIQUE (tenant_id, project_id, name),
    -- Target for composite foreign keys from child tables.
    UNIQUE (id, tenant_id)
);

CREATE INDEX project_kinds_tenant_project_idx
    ON project_kinds (tenant_id, project_id, position);

COMMENT ON TABLE project_kinds IS
    'The kinds a project offers when an item is created: an offer, not a constraint on items.kind.';
COMMENT ON COLUMN project_kinds.name IS
    'The kind name as the project spells it; non-empty and at most 40 characters.';
COMMENT ON COLUMN project_kinds.position IS
    'The order the project offers its kinds in; zero is first.';

-- ---------------------------------------------------------------------------
-- Row-level security: ENABLE + FORCE, exactly like every other tenant table.
-- ---------------------------------------------------------------------------
ALTER TABLE project_kinds ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_kinds FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON project_kinds
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());

-- ---------------------------------------------------------------------------
-- Privileges, spelled out so a later change to the defaults cannot silently
-- change this file's contract.
-- ---------------------------------------------------------------------------
GRANT SELECT, INSERT, UPDATE, DELETE ON project_kinds TO aic_app;
