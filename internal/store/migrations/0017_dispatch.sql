-- 0017_dispatch.sql — the cross-tenant surface the dispatcher needs.
--
-- The dispatcher walks the tenants, looks at each one's queue and starts the
-- work. The stores are tenant-scoped on purpose, so the two questions that
-- cross a tenant boundary — "which tenants are there?" and "how many jobs are
-- the workers running right now?" — are answered by two narrow SECURITY
-- DEFINER functions owned by aic_auth, exactly like worker_claim_job and
-- engine_active_runs before them.
--
-- Both return coordinates only: the engine and the API do the reading and the
-- writing, tenant-scoped, under RLS.

-- ---------------------------------------------------------------------------
-- dispatch_tenants: every tenant with its owner.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.dispatch_tenants()
RETURNS TABLE (tenant_id TEXT, owner_user_id TEXT)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
BEGIN
    RETURN QUERY
    SELECT t.id,
           (SELECT m.user_id
              FROM public.tenant_memberships m
             WHERE m.tenant_id = t.id AND m.role = 'owner'
             ORDER BY m.created_at
             LIMIT 1)
      FROM public.tenants t
     ORDER BY t.created_at;
END
$$;

ALTER FUNCTION public.dispatch_tenants() OWNER TO aic_auth;

REVOKE EXECUTE ON FUNCTION public.dispatch_tenants() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.dispatch_tenants() TO aic_app;

-- ---------------------------------------------------------------------------
-- dispatch_running_jobs: the global worker pool in use.
--
-- The pool is shared by every tenant, so the count has to cross the tenant
-- boundary. aic_auth needs exactly SELECT on the jobs table and nothing else.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.dispatch_running_jobs()
RETURNS INT
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    v_count INT;
BEGIN
    SELECT count(*) INTO v_count
      FROM public.agent_jobs
     WHERE state = 'running';
    RETURN v_count;
END
$$;

ALTER FUNCTION public.dispatch_running_jobs() OWNER TO aic_auth;
GRANT SELECT ON public.agent_jobs TO aic_auth;

REVOKE EXECUTE ON FUNCTION public.dispatch_running_jobs() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.dispatch_running_jobs() TO aic_app;
