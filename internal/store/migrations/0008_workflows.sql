-- 0008_workflows.sql — durable workflows, checkpoints, memory and notifications.
--
-- Every tenant-owned row is protected by ENABLE + FORCE RLS, and every
-- tenant-owned parent reference is composite so a cross-tenant link cannot be
-- represented in the database.

-- ---------------------------------------------------------------------------
-- Workflow definitions and durable executions.
-- ---------------------------------------------------------------------------
CREATE TABLE workflow_templates (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name            TEXT        NOT NULL,
    version         INT         NOT NULL DEFAULT 1,
    applies_to      TEXT        NOT NULL DEFAULT '',
    graph_json      JSONB       NOT NULL,
    max_concurrency INT         NOT NULL DEFAULT 4
                                CHECK (max_concurrency BETWEEN 1 AND 32),
    created_by      TEXT        REFERENCES users (id) ON DELETE SET NULL,
    published_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, name, version),
    UNIQUE (id, tenant_id)
);

CREATE OR REPLACE FUNCTION public.workflow_templates_published_immutable()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
    IF OLD.published_at IS NOT NULL THEN
        RAISE EXCEPTION 'published_workflow_template_is_immutable'
            USING ERRCODE = 'AIC07';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER workflow_templates_published_no_mutation
    BEFORE UPDATE OR DELETE ON workflow_templates
    FOR EACH ROW EXECUTE FUNCTION public.workflow_templates_published_immutable();

CREATE TABLE workflow_runs (
    id                       TEXT PRIMARY KEY,
    tenant_id                TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    template_id              TEXT        NOT NULL,
    template_version         INT         NOT NULL,
    item_id                  TEXT,
    definition_snapshot_json JSONB       NOT NULL,
    state                    TEXT        NOT NULL DEFAULT 'queued'
                                         CHECK (state IN ('queued', 'running', 'waiting_human',
                                                          'completed', 'failed', 'cancelled')),
    max_concurrency          INT         NOT NULL DEFAULT 4,
    started_by               TEXT        REFERENCES users (id) ON DELETE SET NULL,
    worktree_path            TEXT,
    branch                   TEXT,
    started_at               TIMESTAMPTZ,
    finished_at              TIMESTAMPTZ,
    error                    TEXT,
    failure_kind             TEXT        CHECK (failure_kind IN ('dependency', 'needs_input',
                                                                  'capability', 'transient')),
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),

    FOREIGN KEY (template_id, tenant_id)
        REFERENCES workflow_templates (id, tenant_id) ON DELETE RESTRICT,
    FOREIGN KEY (item_id, tenant_id)
        REFERENCES items (id, tenant_id) ON DELETE SET NULL (item_id),
    UNIQUE (id, tenant_id)
);

CREATE INDEX workflow_runs_tenant_state_idx ON workflow_runs (tenant_id, state);

CREATE TABLE step_runs (
    id                    TEXT PRIMARY KEY,
    tenant_id             TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    run_id                TEXT        NOT NULL,
    node_id               TEXT        NOT NULL,
    iteration_path        TEXT        NOT NULL DEFAULT '',
    run_index             INT         NOT NULL DEFAULT 0,
    kind                  TEXT        NOT NULL
                                      CHECK (kind IN ('agent', 'integration', 'checkpoint',
                                                      'loop', 'merge')),
    name                  TEXT        NOT NULL DEFAULT '',
    state                 TEXT        NOT NULL DEFAULT 'pending'
                                      CHECK (state IN ('pending', 'ready', 'running', 'waiting',
                                                       'succeeded', 'failed', 'skipped', 'blocked',
                                                       'cancelled', 'superseded')),
    actor_kind            TEXT        CHECK (actor_kind IN ('human', 'agent', 'system')),
    actor_id              TEXT,
    input_edges_json      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    memory_manifest_json  JSONB,
    context_manifest_json JSONB,
    task_file_sha         TEXT,
    output_ports_json     JSONB,
    result_json           JSONB,
    failure_kind          TEXT        CHECK (failure_kind IN ('dependency', 'needs_input',
                                                              'capability', 'transient')),
    workspace_kind        TEXT        NOT NULL DEFAULT 'scratch'
                                      CHECK (workspace_kind IN ('scratch', 'worktree', 'dir')),
    workdir               TEXT,
    agent_job_id          TEXT,
    resume_token          TEXT,
    claim_lock            TEXT,
    claim_expires_at      TIMESTAMPTZ,
    heartbeat_at          TIMESTAMPTZ,
    superseded_by         TEXT,
    started_at            TIMESTAMPTZ,
    finished_at           TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),

    FOREIGN KEY (run_id, tenant_id)
        REFERENCES workflow_runs (id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (agent_job_id, tenant_id)
        REFERENCES agent_jobs (id, tenant_id) ON DELETE SET NULL (agent_job_id),
    FOREIGN KEY (superseded_by, tenant_id)
        REFERENCES step_runs (id, tenant_id) ON DELETE SET NULL (superseded_by),
    UNIQUE (run_id, node_id, iteration_path, run_index),
    UNIQUE (id, tenant_id)
);

CREATE INDEX step_runs_run_state_idx ON step_runs (run_id, state);
CREATE INDEX step_runs_tenant_state_idx ON step_runs (tenant_id, state);

-- Connect executions to jobs after step_runs exists; the composite link is
-- nullable on deletion without ever nulling the tenant key.
ALTER TABLE agent_jobs ADD COLUMN step_run_id TEXT;
ALTER TABLE agent_jobs ADD CONSTRAINT agent_jobs_step_run_fk
    FOREIGN KEY (step_run_id, tenant_id) REFERENCES step_runs (id, tenant_id)
    ON DELETE SET NULL (step_run_id);
ALTER TABLE agent_jobs ADD COLUMN idempotency_key TEXT;
CREATE UNIQUE INDEX agent_jobs_idempotency_key_idx ON agent_jobs (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Human gates and interventions.
-- ---------------------------------------------------------------------------
CREATE TABLE checkpoints (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    run_id           TEXT        NOT NULL,
    step_run_id      TEXT        NOT NULL,
    node_id          TEXT        NOT NULL,
    lap              INT         NOT NULL DEFAULT 1 CHECK (lap >= 1),
    state            TEXT        NOT NULL DEFAULT 'pending'
                                 CHECK (state IN ('pending', 'approved', 'approved_with_edits',
                                                  'rejected', 'aborted', 'superseded')),
    summary_md       TEXT        NOT NULL DEFAULT '',
    decision_md      TEXT,
    fields_json      JSONB,
    decided_by_kind  TEXT        CHECK (decided_by_kind IN ('human', 'agent', 'system')),
    decided_by       TEXT,
    decided_at       TIMESTAMPTZ,
    remind_after     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    FOREIGN KEY (run_id, tenant_id)
        REFERENCES workflow_runs (id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (step_run_id, tenant_id)
        REFERENCES step_runs (id, tenant_id) ON DELETE CASCADE,
    UNIQUE (step_run_id, lap),
    UNIQUE (id, tenant_id)
);

CREATE INDEX checkpoints_pending_tenant_created_idx ON checkpoints (tenant_id, created_at)
    WHERE state = 'pending';

CREATE TABLE interventions (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    run_id       TEXT        NOT NULL,
    node_id      TEXT,
    step_run_id  TEXT,
    agent_job_id TEXT,
    kind         TEXT        NOT NULL CHECK (kind IN ('inject', 'redo', 'stop')),
    body_md      TEXT,
    actor_kind   TEXT        NOT NULL CHECK (actor_kind IN ('human', 'agent', 'system')),
    actor_id     TEXT,
    seq          INT         NOT NULL,
    delivered_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    FOREIGN KEY (run_id, tenant_id)
        REFERENCES workflow_runs (id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (step_run_id, tenant_id)
        REFERENCES step_runs (id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (agent_job_id, tenant_id)
        REFERENCES agent_jobs (id, tenant_id) ON DELETE SET NULL (agent_job_id),
    UNIQUE (step_run_id, seq),
    UNIQUE (id, tenant_id)
);

CREATE INDEX interventions_tenant_created_idx ON interventions (tenant_id, created_at);

-- ---------------------------------------------------------------------------
-- Run outputs and revisioned memory.
-- ---------------------------------------------------------------------------
CREATE TABLE artifacts (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    run_id      TEXT,
    step_run_id TEXT,
    item_id     TEXT,
    kind        TEXT        NOT NULL
                            CHECK (kind IN ('branch', 'commit', 'pr', 'diff_summary', 'file', 'report')),
    uri         TEXT        NOT NULL,
    meta_json   JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    FOREIGN KEY (run_id, tenant_id)
        REFERENCES workflow_runs (id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (step_run_id, tenant_id)
        REFERENCES step_runs (id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (item_id, tenant_id)
        REFERENCES items (id, tenant_id) ON DELETE SET NULL (item_id),
    UNIQUE (id, tenant_id)
);

CREATE TABLE memory_entries (
    id                 TEXT PRIMARY KEY,
    tenant_id          TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    scope_kind         TEXT        NOT NULL CHECK (scope_kind IN ('run', 'item', 'agent', 'workspace')),
    scope_ref          TEXT        NOT NULL,
    key                TEXT        NOT NULL,
    revision           INT         NOT NULL DEFAULT 1 CHECK (revision >= 1),
    content_md         TEXT        NOT NULL DEFAULT '',
    content_json       JSONB,
    source             TEXT        NOT NULL CHECK (source IN ('step_output', 'checkpoint',
                                                              'intervention', 'agent_marker',
                                                              'integration', 'human')),
    source_run_id      TEXT,
    source_step_run_id TEXT,
    source_user_id     TEXT        REFERENCES users (id) ON DELETE SET NULL,
    tags_json          JSONB       NOT NULL DEFAULT '[]'::jsonb,
    token_estimate     INT         NOT NULL DEFAULT 0,
    seq                BIGINT      NOT NULL,
    superseded_by      TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    FOREIGN KEY (source_run_id, tenant_id)
        REFERENCES workflow_runs (id, tenant_id) ON DELETE SET NULL (source_run_id),
    FOREIGN KEY (source_step_run_id, tenant_id)
        REFERENCES step_runs (id, tenant_id) ON DELETE SET NULL (source_step_run_id),
    FOREIGN KEY (superseded_by, tenant_id)
        REFERENCES memory_entries (id, tenant_id) ON DELETE SET NULL (superseded_by),
    UNIQUE (tenant_id, scope_kind, scope_ref, key, revision),
    UNIQUE (id, tenant_id)
);

CREATE INDEX memory_entries_scope_seq_idx
    ON memory_entries (tenant_id, scope_kind, scope_ref, seq);

CREATE OR REPLACE FUNCTION public.memory_entries_append_only()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR (to_jsonb(NEW) - 'superseded_by') IS DISTINCT FROM
          (to_jsonb(OLD) - 'superseded_by') THEN
        RAISE EXCEPTION 'memory_entries_are_append_only'
            USING ERRCODE = 'AIC06';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER memory_entries_append_only_guard
    BEFORE UPDATE OR DELETE ON memory_entries
    FOR EACH ROW EXECUTE FUNCTION public.memory_entries_append_only();

-- ---------------------------------------------------------------------------
-- User notifications and owner/admin-configured command channels.
-- ---------------------------------------------------------------------------
CREATE TABLE notifications (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    user_id     TEXT        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind        TEXT        NOT NULL CHECK (kind IN ('checkpoint_pending', 'run_failed',
                                                     'run_completed', 'sync_error')),
    title_md    TEXT        NOT NULL DEFAULT '',
    body_md     TEXT        NOT NULL DEFAULT '',
    link        TEXT,
    payload_json JSONB      NOT NULL DEFAULT '{}'::jsonb,
    read_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (id, tenant_id)
);

CREATE INDEX notifications_unread_tenant_user_created_idx
    ON notifications (tenant_id, user_id, created_at DESC)
    WHERE read_at IS NULL;

CREATE TABLE notification_channels (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    kind             TEXT        NOT NULL CHECK (kind IN ('command')),
    name             TEXT        NOT NULL,
    command_template TEXT        NOT NULL,
    events_json      JSONB       NOT NULL DEFAULT '[]'::jsonb,
    enabled          BOOLEAN     NOT NULL DEFAULT TRUE,
    created_by       TEXT        REFERENCES users (id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (id, tenant_id)
);

COMMENT ON TABLE notification_channels IS
    'A stored shell command is owner/admin-only configuration; execution is not implemented yet and only owner/admin may create rows (enforced in the API later).';

-- ---------------------------------------------------------------------------
-- Extend threads with run and checkpoint contexts. Drop the legacy auto-named
-- CHECKs by inspecting their definitions, then install stable constraint names.
-- ---------------------------------------------------------------------------
ALTER TABLE threads ADD COLUMN run_id TEXT;

DO $$
DECLARE
    constraint_row RECORD;
BEGIN
    FOR constraint_row IN
        SELECT c.conname
          FROM pg_constraint c
         WHERE c.conrelid = 'public.threads'::regclass
           AND c.contype = 'c'
           AND (pg_get_constraintdef(c.oid) ILIKE '%context_kind%'
                OR pg_get_constraintdef(c.oid) ILIKE '%free%')
    LOOP
        EXECUTE format('ALTER TABLE public.threads DROP CONSTRAINT %I', constraint_row.conname);
    END LOOP;
END
$$;

ALTER TABLE threads
    ADD CONSTRAINT threads_context_kind_check
        CHECK (context_kind IN ('item', 'product', 'free', 'run', 'checkpoint')),
    ADD CONSTRAINT threads_context_shape_check
        CHECK ((context_kind = 'item'       AND item_id IS NOT NULL AND product_id IS NULL AND run_id IS NULL)
            OR (context_kind = 'product'    AND product_id IS NOT NULL AND item_id IS NULL AND run_id IS NULL)
            OR (context_kind = 'free'       AND item_id IS NULL AND product_id IS NULL AND run_id IS NULL)
            OR (context_kind IN ('run', 'checkpoint') AND run_id IS NOT NULL
                                                  AND item_id IS NULL AND product_id IS NULL));

ALTER TABLE threads ADD CONSTRAINT threads_run_fk
    FOREIGN KEY (run_id, tenant_id) REFERENCES workflow_runs (id, tenant_id) ON DELETE CASCADE;

-- Agent assignees are tenant-bound just like project and item parents.
ALTER TABLE items ADD COLUMN assignee_agent_id TEXT;
ALTER TABLE items ADD CONSTRAINT items_assignee_agent_fk
    FOREIGN KEY (assignee_agent_id, tenant_id) REFERENCES agents (id, tenant_id)
    ON DELETE SET NULL (assignee_agent_id);

-- ---------------------------------------------------------------------------
-- Row-level security: every new tenant table is ENABLE + FORCE with the same
-- tenant policy as the existing schema.
-- ---------------------------------------------------------------------------
ALTER TABLE workflow_templates  ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_runs       ENABLE ROW LEVEL SECURITY;
ALTER TABLE step_runs           ENABLE ROW LEVEL SECURITY;
ALTER TABLE checkpoints         ENABLE ROW LEVEL SECURITY;
ALTER TABLE interventions       ENABLE ROW LEVEL SECURITY;
ALTER TABLE artifacts           ENABLE ROW LEVEL SECURITY;
ALTER TABLE memory_entries      ENABLE ROW LEVEL SECURITY;
ALTER TABLE notifications       ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_channels ENABLE ROW LEVEL SECURITY;

ALTER TABLE workflow_templates  FORCE ROW LEVEL SECURITY;
ALTER TABLE workflow_runs       FORCE ROW LEVEL SECURITY;
ALTER TABLE step_runs           FORCE ROW LEVEL SECURITY;
ALTER TABLE checkpoints         FORCE ROW LEVEL SECURITY;
ALTER TABLE interventions       FORCE ROW LEVEL SECURITY;
ALTER TABLE artifacts           FORCE ROW LEVEL SECURITY;
ALTER TABLE memory_entries      FORCE ROW LEVEL SECURITY;
ALTER TABLE notifications       FORCE ROW LEVEL SECURITY;
ALTER TABLE notification_channels FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON workflow_templates
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON workflow_runs
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON step_runs
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON checkpoints
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON interventions
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON artifacts
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON memory_entries
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON notifications
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON notification_channels
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());

-- Explicit app grants. Memory revisions can only be inserted, read, or
-- superseded; its trigger additionally restricts UPDATE to superseded_by.
GRANT SELECT, INSERT, UPDATE, DELETE ON workflow_templates TO aic_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON workflow_runs TO aic_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON step_runs TO aic_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON checkpoints TO aic_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON interventions TO aic_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON artifacts TO aic_app;
GRANT SELECT, INSERT, UPDATE ON memory_entries TO aic_app;
REVOKE DELETE ON memory_entries FROM aic_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON notifications TO aic_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON notification_channels TO aic_app;
