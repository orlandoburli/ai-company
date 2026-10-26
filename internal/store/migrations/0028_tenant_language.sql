-- 0028_tenant_language.sql — the language a company works in, and the language
-- a run remembers.
--
-- A tenant declares the language its work is done in, and its agents are told:
-- their answers, the documents they write, the pull-request titles and bodies
-- they open, the comments they leave and the commit messages they write come
-- back in that language.
--
-- The setting belongs to the tenant, not to a user and not to a run:
--
--   * tenants.language is the company's default. A run inherits it unless the
--     request that starts the run names another language.
--   * workflow_runs.language is what the run actually works in. It is written
--     once, when the run starts, so a company that changes its language
--     halfway through a workflow does not change the language of the work
--     already under way — the run keeps the language it started in.
--
-- The column lives in the identity plane, so this migration only adds it. The
-- RLS policies, the existing grants and the existing definer functions are
-- untouched: nothing about tenancy's isolation changes here.
--
-- The application role has no privileges on `tenants` at all (migration 0002),
-- so the two functions below are the only door to the column. They follow the
-- house pattern of 0003 exactly:
--
--   * SECURITY DEFINER, owned by aic_auth, with a pinned search_path;
--   * keyed on the session token, never on a caller-supplied tenant id, so the
--     tenant is always the one the session acts in;
--   * the caller's role is re-checked from the session's own membership, so a
--     bug in the HTTP layer cannot widen the permission;
--   * the return convention of 0003: NULL means success, a short reason string
--     means refusal.
--
-- The check constraint is the last word on the vocabulary: the product speaks
-- English and Brazilian Portuguese, and an unknown language is refused rather
-- than guessed at.

-- ---------------------------------------------------------------------------
-- tenants.language — the company's working language.
-- ---------------------------------------------------------------------------
ALTER TABLE tenants
    ADD COLUMN language TEXT NOT NULL DEFAULT 'en';

ALTER TABLE tenants
    ADD CONSTRAINT tenants_language_check CHECK (language IN ('en', 'pt-BR'));

COMMENT ON COLUMN tenants.language IS
    'The language this company works in: ''en'' or ''pt-BR''. Every run of the company inherits it unless the request that starts the run names another one, and every agent of that run writes its answers, documents, pull requests, comments and commit messages in it.';

-- ---------------------------------------------------------------------------
-- workflow_runs.language — the language this run works in.
--
-- The column is NOT NULL with a default, so the runs that already exist keep
-- working in English instead of failing on a NULL.
-- ---------------------------------------------------------------------------
ALTER TABLE workflow_runs
    ADD COLUMN language TEXT NOT NULL DEFAULT 'en';

ALTER TABLE workflow_runs
    ADD CONSTRAINT workflow_runs_language_check CHECK (language IN ('en', 'pt-BR'));

COMMENT ON COLUMN workflow_runs.language IS
    'The language this run works in. It is resolved when the run starts — the request may name one, otherwise the tenant''s language is inherited — and it never changes afterwards, so the agents of a running workflow all write in the same language.';

-- ---------------------------------------------------------------------------
-- Reading the language.
--
-- The function is keyed on the session token, so a caller can only ever read
-- the language of the tenant it is acting in.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.auth_tenant_language(p_token_hash TEXT)
RETURNS TEXT
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    SELECT t.language
      FROM public.sessions s
      JOIN public.tenants t ON t.id = s.tenant_id
     WHERE s.token_hash = p_token_hash
       AND s.expires_at > now()
$$;

-- ---------------------------------------------------------------------------
-- Changing the language.
--
-- Same authorization as auth_update_tenant: the role comes from the session's
-- own membership and the tenant from the session, so the function can only
-- ever change the caller's own tenant, and only an owner or an admin may ask.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.auth_update_tenant_language(
    p_token_hash TEXT, p_language TEXT
) RETURNS TEXT
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    v_role TEXT;
    v_tenant TEXT;
BEGIN
    SELECT auth_session_role(p_token_hash) INTO v_role;
    IF v_role IS NULL THEN
        RETURN 'unauthenticated';
    END IF;
    IF v_role NOT IN ('owner', 'admin') THEN
        RETURN 'forbidden';
    END IF;

    IF p_language IS NULL OR p_language NOT IN ('en', 'pt-BR') THEN
        RETURN 'invalid_language';
    END IF;

    SELECT s.tenant_id INTO v_tenant
      FROM public.sessions s
     WHERE s.token_hash = p_token_hash AND s.expires_at > now();

    UPDATE public.tenants
       SET language = p_language
     WHERE id = v_tenant;

    RETURN NULL;
END
$$;

-- ---------------------------------------------------------------------------
-- Ownership and privileges: the same pattern as 0002 and 0003. The functions
-- default to EXECUTE for PUBLIC, which would hand the privileged surface to
-- every role, so the default is revoked first and the grant is made to the one
-- role that needs it.
-- ---------------------------------------------------------------------------
ALTER FUNCTION public.auth_tenant_language(TEXT)              OWNER TO aic_auth;
ALTER FUNCTION public.auth_update_tenant_language(TEXT, TEXT) OWNER TO aic_auth;

REVOKE EXECUTE ON FUNCTION public.auth_tenant_language(TEXT)              FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.auth_update_tenant_language(TEXT, TEXT) FROM PUBLIC;

GRANT EXECUTE ON FUNCTION public.auth_tenant_language(TEXT)              TO aic_app;
GRANT EXECUTE ON FUNCTION public.auth_update_tenant_language(TEXT, TEXT) TO aic_app;
