-- 0004_content.sql — products, projects, items, dependencies, activity log.
--
-- This is the first content schema, so it is where RLS starts doing real work:
-- every table here is ENABLE + FORCE, and the app role owns nothing.
--
-- Two classes of mistake are prevented structurally rather than by convention:
--
--   1. Cross-tenant references. Every parent link is a COMPOSITE foreign key on
--      (id, tenant_id), so an item cannot point at another tenant's project and
--      a dependency cannot connect two tenants' items — the database refuses it,
--      regardless of what the application passes.
--   2. Dependency cycles. A trigger walks the edge graph on insert and rejects
--      an edge that closes a path back to the dependent, with a dedicated
--      SQLSTATE the API maps to 409. Cycle repair by silently dropping edges is
--      never an option.

-- ---------------------------------------------------------------------------
-- Per-tenant counters. Item numbers are human-facing ("#42") and must be
-- unique per tenant, not globally. A counter row is the serialisation point:
-- the UPDATE takes a row lock, so concurrent inserts get distinct numbers
-- without a table-wide lock or a max()+1 race.
-- ---------------------------------------------------------------------------
CREATE TABLE tenant_counters (
    tenant_id TEXT   NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name      TEXT   NOT NULL,
    value     BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, name)
);

-- ---------------------------------------------------------------------------
-- products
-- ---------------------------------------------------------------------------
CREATE TABLE products (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name             TEXT        NOT NULL,
    slug             TEXT        NOT NULL,
    repo_url         TEXT,
    jira_project_key TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, slug),
    -- Target for composite foreign keys from child tables.
    UNIQUE (id, tenant_id)
);

CREATE INDEX products_tenant_idx ON products (tenant_id, created_at);

-- ---------------------------------------------------------------------------
-- projects: recursive, nested inside a product.
--
-- The product reference is composite, so a project can never be attached to a
-- product in another tenant. parent_id is likewise composite.
-- ---------------------------------------------------------------------------
CREATE TABLE projects (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    product_id  TEXT        NOT NULL,
    parent_id   TEXT,
    name        TEXT        NOT NULL,
    key         TEXT,
    description TEXT,
    status      TEXT        NOT NULL DEFAULT 'active'
                            CHECK (status IN ('active', 'archived')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    FOREIGN KEY (product_id, tenant_id) REFERENCES products (id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (parent_id, tenant_id)  REFERENCES projects (id, tenant_id) ON DELETE CASCADE,

    -- A project cannot be its own parent. Deeper cycles are prevented by the
    -- re-parent path, which reuses the item/dependency walk's shape.
    CHECK (parent_id IS NULL OR parent_id <> id),
    UNIQUE (id, tenant_id)
);

CREATE INDEX projects_tenant_product_idx ON projects (tenant_id, product_id, created_at);
CREATE INDEX projects_tenant_parent_idx  ON projects (tenant_id, parent_id);

-- ---------------------------------------------------------------------------
-- items: work items, arbitrarily nested. kind is free-form by design -- there is
-- deliberately no enum, because users name their own kinds.
-- ---------------------------------------------------------------------------
CREATE TABLE items (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    project_id       TEXT        NOT NULL,
    number           BIGINT      NOT NULL,
    parent_id        TEXT,

    kind             TEXT        NOT NULL,
    title            TEXT        NOT NULL,
    description_md   TEXT        NOT NULL DEFAULT '',
    status           TEXT        NOT NULL DEFAULT 'backlog'
                                 CHECK (status IN ('backlog', 'todo', 'in_progress',
                                                   'waiting_human', 'in_review',
                                                   'done', 'cancelled')),

    -- Manual priority is a normalised label, never a ranker output (FR-PRI-2).
    priority         TEXT        CHECK (priority IN ('priority:p0', 'priority:p1', 'priority:p2')),
    priority_source  TEXT        CHECK (priority_source IN ('manual', 'inherited')),

    -- Human queue overrides. Pause is eligibility, not failure; pin is ordering
    -- only. Both are attributed and survive restarts (FR-QUE-1..7).
    paused_at        TIMESTAMPTZ,
    paused_by        TEXT REFERENCES users (id) ON DELETE SET NULL,
    pause_reason     TEXT,
    pinned_at        TIMESTAMPTZ,
    pinned_by        TEXT REFERENCES users (id) ON DELETE SET NULL,

    assignee_kind    TEXT        CHECK (assignee_kind IN ('human', 'agent')),
    assignee_user_id TEXT REFERENCES users (id) ON DELETE SET NULL,

    labels_json      JSONB       NOT NULL DEFAULT '[]'::jsonb,
    due_date         DATE,
    github_issue_url TEXT,
    jira_issue_key   TEXT,
    external_url     TEXT,

    sort_order       DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    FOREIGN KEY (project_id, tenant_id) REFERENCES projects (id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (parent_id, tenant_id)  REFERENCES items (id, tenant_id) ON DELETE CASCADE,

    CHECK (parent_id IS NULL OR parent_id <> id),
    -- A paused item must record who paused it, and a pin who pinned it: an
    -- unattributed overrides is indistinguishable from a bug.
    CHECK ((paused_at IS NULL) = (paused_by IS NULL)),
    CHECK ((pinned_at IS NULL) = (pinned_by IS NULL)),

    UNIQUE (tenant_id, number),
    UNIQUE (id, tenant_id)
);

CREATE INDEX items_tenant_project_idx ON items (tenant_id, project_id, status);
CREATE INDEX items_tenant_parent_idx  ON items (tenant_id, parent_id);
CREATE INDEX items_tenant_status_idx  ON items (tenant_id, status);
-- Dispatch indexes named in §3: pinned items first, then the paused set.
CREATE INDEX items_tenant_pinned_idx ON items (tenant_id, pinned_at DESC)
    WHERE pinned_at IS NOT NULL;
CREATE INDEX items_tenant_paused_idx ON items (tenant_id, paused_at)
    WHERE paused_at IS NOT NULL;

-- ---------------------------------------------------------------------------
-- item_dependencies: first-class blocked_by edges, never a label or a counter.
-- ---------------------------------------------------------------------------
CREATE TABLE item_dependencies (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    from_item_id TEXT        NOT NULL,   -- the dependent
    to_item_id   TEXT        NOT NULL,   -- the blocker it waits on
    kind         TEXT        NOT NULL DEFAULT 'blocked_by'
                             CHECK (kind IN ('blocked_by')),
    created_by   TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Composite again: an edge cannot span tenants even if both ids exist.
    FOREIGN KEY (from_item_id, tenant_id) REFERENCES items (id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (to_item_id, tenant_id)   REFERENCES items (id, tenant_id) ON DELETE CASCADE,

    CHECK (from_item_id <> to_item_id),
    UNIQUE (tenant_id, from_item_id, to_item_id, kind)
);

-- Both directions are walked by recursive CTEs (dependents, dependencies,
-- unblocking power), so both get an index.
CREATE INDEX item_deps_tenant_from_idx ON item_dependencies (tenant_id, from_item_id);
CREATE INDEX item_deps_tenant_to_idx   ON item_dependencies (tenant_id, to_item_id);

-- Cycle rejection on insert. The walk starts at the blocker and follows its own
-- blocked_by edges; if it reaches the dependent, the new edge would close a
-- cycle. Self-edges are caught by the CHECK above, but the trigger reports them
-- with the same shape so the API has one mapping.
CREATE OR REPLACE FUNCTION public.check_item_dependency() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
    IF NEW.from_item_id = NEW.to_item_id THEN
        RAISE EXCEPTION 'self_dependency' USING ERRCODE = 'AIC02';
    END IF;

    IF EXISTS (
        WITH RECURSIVE chain(id) AS (
            SELECT NEW.to_item_id
            UNION
            SELECT d.to_item_id
              FROM public.item_dependencies d
              JOIN chain c ON d.from_item_id = c.id
             WHERE d.tenant_id = NEW.tenant_id
        )
        SELECT 1 FROM chain WHERE id = NEW.from_item_id
    ) THEN
        RAISE EXCEPTION 'dependency_cycle' USING ERRCODE = 'AIC01';
    END IF;

    RETURN NEW;
END
$$;

CREATE TRIGGER item_dependencies_no_cycles
    BEFORE INSERT ON item_dependencies
    FOR EACH ROW EXECUTE FUNCTION public.check_item_dependency();

-- ---------------------------------------------------------------------------
-- activity_log: immutable. Every mutation records actor, timestamp and
-- before/after (FR-ACT-4). "Immutable" is enforced, not documented: the app role
-- loses UPDATE and DELETE, and a trigger refuses both for anyone else short of a
-- superuser doing it deliberately.
-- ---------------------------------------------------------------------------
CREATE TABLE activity_log (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    item_id       TEXT,
    actor_kind    TEXT        NOT NULL CHECK (actor_kind IN ('user', 'agent', 'system')),
    actor_user_id TEXT REFERENCES users (id) ON DELETE SET NULL,
    action        TEXT        NOT NULL,
    before_json   JSONB,
    after_json    JSONB,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX activity_log_tenant_item_idx ON activity_log (tenant_id, item_id, created_at DESC);

CREATE OR REPLACE FUNCTION public.activity_log_is_append_only() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
    RAISE EXCEPTION 'activity_log is append-only' USING ERRCODE = 'AIC03';
END
$$;

CREATE TRIGGER activity_log_no_update
    BEFORE UPDATE OR DELETE ON activity_log
    FOR EACH ROW EXECUTE FUNCTION public.activity_log_is_append_only();

-- ---------------------------------------------------------------------------
-- Row-level security. FORCE everywhere: the app role owns nothing, and the
-- owner is bound too.
-- ---------------------------------------------------------------------------
ALTER TABLE tenant_counters     ENABLE ROW LEVEL SECURITY;
ALTER TABLE products            ENABLE ROW LEVEL SECURITY;
ALTER TABLE projects            ENABLE ROW LEVEL SECURITY;
ALTER TABLE items               ENABLE ROW LEVEL SECURITY;
ALTER TABLE item_dependencies   ENABLE ROW LEVEL SECURITY;
ALTER TABLE activity_log        ENABLE ROW LEVEL SECURITY;

ALTER TABLE tenant_counters     FORCE ROW LEVEL SECURITY;
ALTER TABLE products            FORCE ROW LEVEL SECURITY;
ALTER TABLE projects            FORCE ROW LEVEL SECURITY;
ALTER TABLE items               FORCE ROW LEVEL SECURITY;
ALTER TABLE item_dependencies   FORCE ROW LEVEL SECURITY;
ALTER TABLE activity_log        FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON tenant_counters
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON products
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON projects
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON items
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON item_dependencies
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON activity_log
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());

-- The append-only guarantee, at the privilege layer as well.
REVOKE UPDATE, DELETE ON activity_log FROM aic_app;
