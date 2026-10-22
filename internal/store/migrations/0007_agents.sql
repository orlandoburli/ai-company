-- 0007_agents.sql — agents, chat (threads/messages), agent jobs, job logs.
--
-- This is the M2 persistence layer for the agent runtime (mvp-design.md §6).
-- It follows the discipline of 0004: every tenant table is ENABLE + FORCE RLS,
-- and every parent link is a COMPOSITE foreign key on (id, tenant_id), so a
-- cross-tenant reference is refused by the database regardless of what the
-- application passes.
--
-- The one deliberate exception to tenant scoping is job claiming: a background
-- worker has no session, so it cannot set app.tenant_id. The claim/requeue
-- surface is two SECURITY DEFINER functions owned by aic_auth (the 0002
-- pattern) that return only job coordinates — there is still no privileged
-- connection. Everything after a claim runs tenant-scoped under RLS.

-- ---------------------------------------------------------------------------
-- agents
--
-- name must be parseable as an @mention ("@dev"), hence the lowercase slug
-- CHECK; UNIQUE (tenant_id, name) keeps mentions unambiguous per tenant.
-- config_json/capabilities_json/skills_policy_json/fallbacks_json are opaque:
-- the schema stores JSON, the connector layer interprets it per kind.
-- ---------------------------------------------------------------------------
CREATE TABLE agents (
    id                TEXT PRIMARY KEY,
    tenant_id         TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name              TEXT        NOT NULL
                                  CHECK (name ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    role              TEXT        NOT NULL
                                  CHECK (role IN ('pm', 'dev', 'reviewer', 'qa', 'researcher')),
    kind              TEXT        NOT NULL
                                  CHECK (kind IN ('cli', 'api')),
    config_json       JSONB       NOT NULL DEFAULT '{}',
    capabilities_json JSONB       NOT NULL DEFAULT '{}',
    concurrency_limit INT         NOT NULL DEFAULT 2
                                  CHECK (concurrency_limit BETWEEN 1 AND 16),
    enabled           BOOLEAN     NOT NULL DEFAULT TRUE,
    soul_md           TEXT        NOT NULL DEFAULT '',
    skills_policy_json JSONB      NOT NULL DEFAULT '{}',
    fallbacks_json    JSONB       NOT NULL DEFAULT '[]',
    created_by        TEXT        REFERENCES users (id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, name),
    UNIQUE (id, tenant_id)          -- target for composite foreign keys
);

CREATE INDEX agents_tenant_idx ON agents (tenant_id, name);

-- ---------------------------------------------------------------------------
-- threads: a conversation bound to an item, a product, or nothing (free).
-- The context kinds (and the shape CHECKs) match internal/agent/types.go; the
-- run/checkpoint kinds of §3 arrive with M3 and are deliberately not accepted
-- yet, so a wider CHECK cannot silently accept a kind nothing reads.
-- ---------------------------------------------------------------------------
CREATE TABLE threads (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    context_kind TEXT       NOT NULL
                            CHECK (context_kind IN ('item', 'product', 'free')),
    item_id     TEXT,
    product_id  TEXT,
    title       TEXT        NOT NULL DEFAULT '',
    created_by  TEXT        REFERENCES users (id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Composite, so a thread can never point at another tenant's item/product.
    FOREIGN KEY (item_id, tenant_id)    REFERENCES items (id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (product_id, tenant_id) REFERENCES products (id, tenant_id) ON DELETE CASCADE,

    -- Exactly one context shape per kind.
    CHECK ((context_kind = 'item'    AND item_id IS NOT NULL AND product_id IS NULL)
        OR (context_kind = 'product' AND product_id IS NOT NULL AND item_id IS NULL)
        OR (context_kind = 'free'    AND item_id IS NULL AND product_id IS NULL)),

    UNIQUE (id, tenant_id)
);

-- One thread per item, ever: the get-or-create path relies on this.
CREATE UNIQUE INDEX threads_item_uniq ON threads (tenant_id, item_id)
    WHERE context_kind = 'item';

CREATE INDEX threads_tenant_idx ON threads (tenant_id, created_at);
CREATE INDEX threads_tenant_product_idx ON threads (tenant_id, product_id, created_at);

-- ---------------------------------------------------------------------------
-- messages: one entry in a thread's timeline. seq is a global BIGSERIAL: it is
-- the ordering cursor the API paginates with (after_seq) and the SSE replay
-- resumes from. Author shape is enforced by CHECK, same structurally-prevented
-- style as the thread context shape above.
-- ---------------------------------------------------------------------------
CREATE TABLE messages (
    id              TEXT PRIMARY KEY,
    seq             BIGSERIAL,
    tenant_id       TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    thread_id       TEXT        NOT NULL,
    author_kind     TEXT        NOT NULL CHECK (author_kind IN ('human', 'agent', 'system')),
    author_user_id  TEXT,
    author_agent_id TEXT,
    body_md         TEXT        NOT NULL,
    artifacts_json  JSONB       NOT NULL DEFAULT '[]',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    FOREIGN KEY (thread_id, tenant_id) REFERENCES threads (id, tenant_id) ON DELETE CASCADE,

    -- ON DELETE SET NULL (author_agent_id): column-list syntax (PG >= 15) nulls
    -- only the author column. A bare SET NULL would also null tenant_id, which
    -- is NOT NULL and would break the very isolation composite protects.
    FOREIGN KEY (author_agent_id, tenant_id) REFERENCES agents (id, tenant_id)
        ON DELETE SET NULL (author_agent_id),
    FOREIGN KEY (author_user_id) REFERENCES users (id) ON DELETE SET NULL,

    -- Author shape per kind.
    CHECK ((author_kind = 'human'  AND author_user_id IS NOT NULL AND author_agent_id IS NULL)
        OR (author_kind = 'agent'  AND author_agent_id IS NOT NULL AND author_user_id IS NULL)
        OR (author_kind = 'system' AND author_user_id IS NULL AND author_agent_id IS NULL)),

    UNIQUE (id, tenant_id)
);

CREATE INDEX messages_tenant_thread_idx ON messages (tenant_id, thread_id, seq);

-- ---------------------------------------------------------------------------
-- agent_jobs: one adapter execution. Claims use leases (claim_lock /
-- claim_expires_at, heartbeat_at) exactly as §6 specifies; attempt is 1-based
-- and transient failures requeue the same row rather than spawning new ones.
--
-- Deletion policy per FK:
--   * agents     RESTRICT                 — a job is execution history; an
--     agent with jobs cannot be deleted (the store maps this to 409),
--   * threads    CASCADE                  — chat context dies with the chat,
--   * messages   CASCADE                  — same,
--   * items      SET NULL (item_id)       — the job audit survives item removal,
--     which a CASCADE (history lost) or RESTRICT (item stuck undeletable)
--     would not give. Column-list syntax needs PG >= 15; on an older server
--     drop the column list and use RESTRICT instead.
-- ---------------------------------------------------------------------------
CREATE TABLE agent_jobs (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    agent_id         TEXT        NOT NULL,
    thread_id        TEXT,
    message_id       TEXT,
    item_id          TEXT,
    state            TEXT        NOT NULL DEFAULT 'queued'
                                 CHECK (state IN ('queued', 'running', 'succeeded',
                                                  'failed', 'cancelled')),
    attempt          INT         NOT NULL DEFAULT 0,
    max_attempts     INT         NOT NULL DEFAULT 3
                                 CHECK (max_attempts > 0),
    prompt           TEXT        NOT NULL CHECK (prompt <> ''),
    context_json     JSONB       NOT NULL DEFAULT '{}',
    workdir          TEXT,
    tokens_in        INT         NOT NULL DEFAULT 0,
    tokens_out       INT         NOT NULL DEFAULT 0,
    cost_usd         NUMERIC(12,6),
    result_json      JSONB,
    failure_kind     TEXT,
    error_text       TEXT,
    created_by       TEXT        REFERENCES users (id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at       TIMESTAMPTZ,
    finished_at      TIMESTAMPTZ,
    claim_lock       TEXT,
    claim_expires_at TIMESTAMPTZ,
    heartbeat_at     TIMESTAMPTZ,
    next_attempt_at  TIMESTAMPTZ,

    FOREIGN KEY (agent_id, tenant_id) REFERENCES agents (id, tenant_id) ON DELETE RESTRICT,
    FOREIGN KEY (thread_id, tenant_id) REFERENCES threads (id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (message_id, tenant_id) REFERENCES messages (id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (item_id, tenant_id) REFERENCES items (id, tenant_id)
        ON DELETE SET NULL (item_id),

    -- Light state/time coherence: finished_at only in terminal states, and a
    -- job that started must not reappear as queued (requeue clears
    -- started_at; a running job always carries a claim).
    CHECK ((state IN ('succeeded', 'failed', 'cancelled')) = (finished_at IS NOT NULL)),
    CHECK (started_at IS NULL OR state <> 'queued'),
    CHECK (state <> 'running' OR (claim_lock IS NOT NULL AND claim_expires_at IS NOT NULL)),

    UNIQUE (id, tenant_id)
);

-- Dispatch: per-tenant state views, per-agent concurrency checks, and the
-- claim query the worker runs.
CREATE INDEX agent_jobs_tenant_state_idx ON agent_jobs (tenant_id, state, created_at);
CREATE INDEX agent_jobs_agent_state_idx ON agent_jobs (agent_id, state);
CREATE INDEX agent_jobs_claimable_idx ON agent_jobs (state, next_attempt_at, created_at);

-- ---------------------------------------------------------------------------
-- job_logs: append-only agent output.
--
-- Deliberately NOT partitioned in M2, despite §3's "PARTITION BY RANGE
-- (created_at), monthly". Reason: 0001 sets a blanket ALTER DEFAULT
-- PRIVILEGES granting aic_app full DML on future tables. A partition created
-- later would inherit that grant DIRECTLY, and the parent's RLS policy does
-- not apply to partitions — every partition would be a cross-tenant read hole
-- unless each one dispenses its own RLS + revokes at creation time.
--
-- Correctness wins over the performance detail. Partitioning is deferred until
-- a migration path exists that creates partitions WITH per-partition RLS and
-- explicit revokes (or a default-privileges rework). At M2 volumes a single
-- table with the indexes below is years away from being the bottleneck.
-- ---------------------------------------------------------------------------
CREATE TABLE job_logs (
    seq         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id   TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    job_id      TEXT        NOT NULL,
    stream      TEXT        CHECK (stream IN ('stdout', 'stderr', 'engine', 'adapter')),
    level       TEXT        CHECK (level IN ('debug', 'info', 'warn', 'error')),
    kind        TEXT        CHECK (kind IN ('output_chunk', 'tool_call', 'tool_result',
                                             'usage', 'lifecycle', 'error')),
    message     TEXT        NOT NULL DEFAULT '',
    data_json   JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    FOREIGN KEY (job_id, tenant_id) REFERENCES agent_jobs (id, tenant_id) ON DELETE CASCADE
);

CREATE INDEX job_logs_job_idx ON job_logs (job_id, seq);
CREATE INDEX job_logs_tenant_idx ON job_logs (tenant_id, created_at);

CREATE OR REPLACE FUNCTION public.job_logs_is_append_only() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
    RAISE EXCEPTION 'job_logs_append_only' USING ERRCODE = 'AIC05';
END
$$;

CREATE TRIGGER job_logs_no_update
    BEFORE UPDATE OR DELETE ON job_logs
    FOR EACH ROW EXECUTE FUNCTION public.job_logs_is_append_only();

-- ---------------------------------------------------------------------------
-- Cross-tenant worker functions.
--
-- The claim/requeue is the one operation that cannot be tenant-scoped: a
-- background worker has no session, so it cannot set app.tenant_id. Exactly
-- like the auth functions in 0002, the surface is narrow SECURITY DEFINER
-- functions owned by aic_auth (NOLOGIN, BYPASSRLS). They return only job
-- coordinates; every subsequent read/write on a claimed job runs tenant-scoped
-- under RLS on the app role. The runtime still has no privileged connection.
--
-- Both functions pin search_path and fully qualify their tables.
--
-- worker_claim_job: oldest eligible queued job, FOR UPDATE SKIP LOCKED so
-- concurrent workers never fight over a row; flips it to running inside the
-- same transaction. Every predicate below is qualified with the table name:
-- plpgsql exposes the RETURNS TABLE names as variables, so an unqualified
-- `tenant_id = ` would be an ambiguous reference (SQLSTATE 42702).
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.worker_claim_job(
    p_worker TEXT, p_lease_sec INT, p_skip_agents TEXT[] DEFAULT '{}'
) RETURNS TABLE (tenant_id TEXT, job_id TEXT)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    v_tenant TEXT;
    v_job    TEXT;
BEGIN
    SELECT j.tenant_id, j.id INTO v_tenant, v_job
      FROM public.agent_jobs j
     WHERE j.state = 'queued'
       AND (j.next_attempt_at IS NULL OR j.next_attempt_at <= now())
       AND NOT (j.agent_id = ANY (coalesce(p_skip_agents, '{}')))
     ORDER BY j.created_at, j.id
     FOR UPDATE SKIP LOCKED
     LIMIT 1;

    IF NOT FOUND THEN
        RETURN;
    END IF;

    UPDATE public.agent_jobs
       SET state = 'running',
           attempt = attempt + 1,
           started_at = now(),
           claim_lock = p_worker,
           claim_expires_at = now() + make_interval(secs => p_lease_sec),
           heartbeat_at = now(),
           next_attempt_at = NULL
     WHERE agent_jobs.id = v_job AND agent_jobs.tenant_id = v_tenant;

    RETURN QUERY SELECT v_tenant, v_job;
END
$$;

-- worker_reap_jobs: every running job whose lease expired is either requeued
-- (attempt < p_max_attempts) or failed as a transient lease loss. One
-- statement, so SKIP LOCKED keeps concurrent reapers out of each other's way.
CREATE OR REPLACE FUNCTION public.worker_reap_jobs(p_max_attempts INT)
RETURNS TABLE (tenant_id TEXT, job_id TEXT, state TEXT)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
BEGIN
    RETURN QUERY
    WITH expired AS (
        SELECT j.id, j.tenant_id, j.attempt
          FROM public.agent_jobs j
         WHERE j.state = 'running'
           AND j.claim_expires_at IS NOT NULL
           AND j.claim_expires_at <= now()
         ORDER BY j.claim_expires_at
         FOR UPDATE SKIP LOCKED
    )
    UPDATE public.agent_jobs j
       SET state           = CASE WHEN e.attempt < p_max_attempts THEN 'queued' ELSE 'failed' END,
           next_attempt_at = CASE WHEN e.attempt < p_max_attempts THEN now() END,
           started_at      = CASE WHEN e.attempt < p_max_attempts THEN NULL ELSE j.started_at END,
           finished_at     = CASE WHEN e.attempt < p_max_attempts THEN NULL ELSE now() END,
           error_text      = CASE WHEN e.attempt < p_max_attempts THEN j.error_text ELSE 'lease expired' END,
           failure_kind    = CASE WHEN e.attempt < p_max_attempts THEN j.failure_kind ELSE 'transient' END,
           claim_lock      = NULL,
           claim_expires_at = NULL,
           heartbeat_at    = NULL
      FROM expired e
     WHERE j.id = e.id AND j.tenant_id = e.tenant_id
    RETURNING j.tenant_id, j.id, j.state;
END
$$;

-- aic_auth owns the functions and needs exactly what they touch on agent_jobs
-- (SELECT to pick, UPDATE to transition): nothing more, and nothing on any
-- other tenant table.
ALTER FUNCTION public.worker_claim_job(TEXT, INT, TEXT[]) OWNER TO aic_auth;
ALTER FUNCTION public.worker_reap_jobs(INT) OWNER TO aic_auth;

GRANT SELECT, UPDATE ON public.agent_jobs TO aic_auth;

REVOKE EXECUTE ON FUNCTION public.worker_claim_job(TEXT, INT, TEXT[]) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.worker_reap_jobs(INT) FROM PUBLIC;

GRANT EXECUTE ON FUNCTION public.worker_claim_job(TEXT, INT, TEXT[]) TO aic_app;
GRANT EXECUTE ON FUNCTION public.worker_reap_jobs(INT) TO aic_app;

-- ---------------------------------------------------------------------------
-- Row-level security: ENABLE + FORCE on every table in this file. The app role
-- owns nothing and lacks BYPASSRLS, so a forgotten WHERE tenant_id is an
-- empty result, never another tenant's rows.
-- ---------------------------------------------------------------------------
ALTER TABLE agents     ENABLE ROW LEVEL SECURITY;
ALTER TABLE threads    ENABLE ROW LEVEL SECURITY;
ALTER TABLE messages   ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE job_logs   ENABLE ROW LEVEL SECURITY;

ALTER TABLE agents     FORCE ROW LEVEL SECURITY;
ALTER TABLE threads    FORCE ROW LEVEL SECURITY;
ALTER TABLE messages   FORCE ROW LEVEL SECURITY;
ALTER TABLE agent_jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE job_logs   FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON agents
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON threads
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON messages
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON agent_jobs
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON job_logs
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());

-- ---------------------------------------------------------------------------
-- Privileges. 0001's default privileges would grant aic_app the usual DML on
-- every new table anyway; these are spelled out so a later change to the
-- defaults cannot silently change this file's contract. job_logs is the
-- exception: SELECT and INSERT only, with UPDATE/DELETE revoked explicitly —
-- immutability is enforced at the privilege layer, not just the trigger.
-- ---------------------------------------------------------------------------
GRANT SELECT, INSERT, UPDATE, DELETE ON agents     TO aic_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON threads    TO aic_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON messages   TO aic_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON agent_jobs TO aic_app;
GRANT SELECT, INSERT                   ON job_logs TO aic_app;
REVOKE UPDATE, DELETE                  ON job_logs FROM aic_app;

GRANT USAGE ON SEQUENCE messages_seq_seq TO aic_app;
