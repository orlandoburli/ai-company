-- 0009_engine.sql — engine-facing additions for the M3 workflow engine.
--
-- Two things the engine needs that 0008 did not have:
--
-- 1. job_logs gains run_id/step_run_id. The design (§3) keeps the agent
--    output in one append-only table, indexed by (run_id, created_at) for the
--    run monitor, so a run's output is one query rather than a join across
--    every job of the run. The columns are nullable: a chat job has no run.
--
-- 2. A narrow cross-tenant door for the scheduler. A background scheduler has
--    no session and cannot set app.tenant_id, so — exactly like
--    worker_claim_job in 0007 — the listing of non-terminal runs is a
--    SECURITY DEFINER function owned by aic_auth that returns coordinates
--    only. Every read and write after that runs tenant-scoped under RLS on the
--    app role. The runtime still has no privileged connection, and the
--    function takes no caller-supplied tenant id.
--
-- Concurrency between two schedulers is handled with a session-level advisory
-- lock per run id (pg_try_advisory_lock), which needs no special privilege
-- and no schema.

-- ---------------------------------------------------------------------------
-- job_logs → runs
-- ---------------------------------------------------------------------------
ALTER TABLE job_logs ADD COLUMN run_id TEXT;
ALTER TABLE job_logs ADD COLUMN step_run_id TEXT;

-- The logs are append-only, so the reference is only a label: deleting a run
-- detaches its logs instead of deleting them, and the tenant of the row never
-- changes.
ALTER TABLE job_logs
    ADD CONSTRAINT job_logs_run_fk
        FOREIGN KEY (run_id, tenant_id)
        REFERENCES workflow_runs (id, tenant_id) ON DELETE SET NULL (run_id);

ALTER TABLE job_logs
    ADD CONSTRAINT job_logs_step_run_fk
        FOREIGN KEY (step_run_id, tenant_id)
        REFERENCES step_runs (id, tenant_id) ON DELETE SET NULL (step_run_id);

CREATE INDEX job_logs_run_idx ON job_logs (run_id, seq);

-- ---------------------------------------------------------------------------
-- engine_active_runs: the runs a scheduler tick has to look at.
--
-- It returns the tenant and the run id and nothing else — no graph, no
-- state, no user data — so the surface stays as narrow as the job claimer's.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.engine_active_runs()
RETURNS TABLE (tenant_id TEXT, run_id TEXT)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
BEGIN
    RETURN QUERY
    SELECT r.tenant_id, r.id
      FROM public.workflow_runs r
     WHERE r.state IN ('queued', 'running', 'waiting_human')
     ORDER BY r.created_at, r.id;
END
$$;

-- aic_auth owns the function and needs exactly what it touches: SELECT on the
-- runs table, and nothing on any other tenant table.
ALTER FUNCTION public.engine_active_runs() OWNER TO aic_auth;
GRANT SELECT ON public.workflow_runs TO aic_auth;

REVOKE EXECUTE ON FUNCTION public.engine_active_runs() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.engine_active_runs() TO aic_app;
