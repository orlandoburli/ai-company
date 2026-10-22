-- 0003_tenant_admin.sql — tenant settings and member role management.
--
-- These are the first *writes* to the identity plane from a request path, so they
-- follow the same rule as 0002: no table privileges for aic_app, and every
-- function keyed on the session token rather than on a caller-supplied tenant id.
--
-- Authorisation is enforced twice on purpose: the HTTP layer rejects the wrong
-- role before calling, and the function checks again from the session's own
-- membership. The second check is the one that survives an application bug.
--
-- Return convention: NULL means success; a short reason string means refusal.
-- Reasons are mapped to status codes by the API, and are deliberately coarse --
-- they describe the caller's situation, not the state of other tenants' data.

-- ---------------------------------------------------------------------------
-- Internal helper: the role the session holds in the tenant it is acting in.
-- Not granted to aic_app; only used by the functions below, which run as
-- aic_auth.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.auth_session_role(p_token_hash TEXT)
RETURNS TEXT
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    SELECT m.role
      FROM public.sessions s
      JOIN public.tenant_memberships m
        ON m.user_id = s.user_id AND m.tenant_id = s.tenant_id
     WHERE s.token_hash = p_token_hash
       AND s.expires_at > now()
$$;

-- ---------------------------------------------------------------------------
-- Members of the session's active tenant.
--
-- Readable by any member: names, emails and roles are tenant-visible. Writes are
-- restricted separately.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.auth_list_members(p_token_hash TEXT)
RETURNS TABLE (user_id TEXT, email TEXT, name TEXT, role TEXT, joined_at TIMESTAMPTZ)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    SELECT u.id, u.email, u.name, m.role, m.created_at
      FROM public.sessions s
      JOIN public.tenant_memberships m ON m.tenant_id = s.tenant_id
      JOIN public.users u ON u.id = m.user_id
     WHERE s.token_hash = p_token_hash
       AND s.expires_at > now()
     ORDER BY m.created_at, u.email
$$;

-- ---------------------------------------------------------------------------
-- Rename a tenant or change its handle.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.auth_update_tenant(
    p_token_hash TEXT, p_name TEXT, p_slug TEXT
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

    SELECT s.tenant_id INTO v_tenant
      FROM public.sessions s
     WHERE s.token_hash = p_token_hash AND s.expires_at > now();

    UPDATE public.tenants
       SET name = coalesce(nullif(btrim(p_name), ''), name),
           slug = coalesce(nullif(btrim(p_slug), ''), slug)
     WHERE id = v_tenant;

    RETURN NULL;
END
$$;

-- ---------------------------------------------------------------------------
-- Change a member's role.
--
-- Three invariants this enforces, each of which has been the cause of a real
-- lockout in multi-tenant products:
--   * a member can never change their own role (no self-promotion);
--   * only an owner can grant owner (admins cannot mint peers of their own
--     privilege level);
--   * the last owner cannot be demoted, so a tenant cannot become unmanageable.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.auth_set_member_role(
    p_token_hash TEXT, p_target_user TEXT, p_role TEXT
) RETURNS TEXT
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    v_role TEXT;
    v_tenant TEXT;
    v_user TEXT;
    v_current TEXT;
    v_owners INT;
BEGIN
    SELECT auth_session_role(p_token_hash) INTO v_role;
    IF v_role IS NULL THEN
        RETURN 'unauthenticated';
    END IF;
    IF v_role NOT IN ('owner', 'admin') THEN
        RETURN 'forbidden';
    END IF;

    SELECT s.tenant_id, s.user_id INTO v_tenant, v_user
      FROM public.sessions s
     WHERE s.token_hash = p_token_hash AND s.expires_at > now();

    IF p_role IS NULL OR p_role NOT IN ('owner', 'admin', 'member', 'guest') THEN
        RETURN 'invalid_role';
    END IF;
    IF p_target_user = v_user THEN
        RETURN 'cannot_change_own_role';
    END IF;
    IF p_role = 'owner' AND v_role <> 'owner' THEN
        RETURN 'forbidden';
    END IF;

    SELECT m.role INTO v_current
      FROM public.tenant_memberships m
     WHERE m.tenant_id = v_tenant AND m.user_id = p_target_user;
    IF v_current IS NULL THEN
        RETURN 'not_found';
    END IF;

    IF v_current = 'owner' AND p_role <> 'owner' THEN
        SELECT count(*) INTO v_owners
          FROM public.tenant_memberships m
         WHERE m.tenant_id = v_tenant AND m.role = 'owner';
        IF v_owners <= 1 THEN
            RETURN 'last_owner';
        END IF;
    END IF;

    UPDATE public.tenant_memberships
       SET role = p_role
     WHERE tenant_id = v_tenant AND user_id = p_target_user;

    RETURN NULL;
END
$$;

-- ---------------------------------------------------------------------------
-- Ownership and privileges: same pattern as 0002. Ownership matters here -- a
-- function left owned by the migration role would run with that role's
-- privileges, which in a local install is a superuser and would mask every
-- missing grant below.
-- ---------------------------------------------------------------------------
ALTER FUNCTION public.auth_session_role(TEXT)                    OWNER TO aic_auth;
ALTER FUNCTION public.auth_list_members(TEXT)                    OWNER TO aic_auth;
ALTER FUNCTION public.auth_update_tenant(TEXT, TEXT, TEXT)       OWNER TO aic_auth;
ALTER FUNCTION public.auth_set_member_role(TEXT, TEXT, TEXT)     OWNER TO aic_auth;

-- aic_auth may rename a tenant, and already has UPDATE on memberships from 0002.
GRANT UPDATE ON public.tenants TO aic_auth;

-- --------------------------------------------------------------------------- Revoke from PUBLIC, grant the three
-- reachable functions to aic_app, leave auth_session_role internal.
-- ---------------------------------------------------------------------------
REVOKE EXECUTE ON FUNCTION public.auth_session_role(TEXT)          FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.auth_list_members(TEXT)          FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.auth_update_tenant(TEXT, TEXT, TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.auth_set_member_role(TEXT, TEXT, TEXT) FROM PUBLIC;

GRANT EXECUTE ON FUNCTION public.auth_list_members(TEXT)               TO aic_app;
GRANT EXECUTE ON FUNCTION public.auth_update_tenant(TEXT, TEXT, TEXT)  TO aic_app;
GRANT EXECUTE ON FUNCTION public.auth_set_member_role(TEXT, TEXT, TEXT) TO aic_app;
