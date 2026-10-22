-- 0002_identity_force_rls.sql — remove the privileged connection from the
-- request path, so RLS is FORCEd on every tenant-scoped table.
--
-- The problem this solves:
--
--   0001 left `tenant_memberships` and `sessions` at ENABLE (not FORCE) because
--   login must read memberships before a tenant is known. FORCE binds the table
--   owner as well, so the owner connection would read zero rows and login could
--   never succeed. That left a privileged pool in the request path: correct in
--   practice, but enforced by "we only call it from here" rather than by the
--   database.
--
-- The fix:
--
--   * every tenant-scoped table is ENABLE + FORCE, so even the owner is bound
--     by policy;
--   * the app role loses ALL privileges on the identity tables and reaches them
--     only through SECURITY DEFINER functions owned by `aic_auth`, a NOLOGIN
--     BYPASSRLS role;
--   * there is therefore no privileged *connection* at runtime — just a small,
--     auditable function surface. Each function takes a session token or an
--     email, never a caller-supplied "read any tenant" flag.
--
-- Every function pins `search_path` and fully qualifies its tables. A
-- SECURITY DEFINER function without a pinned search_path is a privilege
-- escalation waiting to happen: the caller can shadow an unqualified name.

-- ---------------------------------------------------------------------------
-- The definer role: no login, no password, no schema privileges of its own.
-- It exists only to own the functions below.
-- ---------------------------------------------------------------------------
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aic_auth') THEN
        CREATE ROLE aic_auth NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE BYPASSRLS;
    END IF;
END
$$;

-- ---------------------------------------------------------------------------
-- Identity functions.
--
-- Credentials never pass through SQL: password verification is Argon2id in Go,
-- so the hash is returned to the caller rather than checked here. The
-- privileged property being granted is "read this row without a tenant scope",
-- not "authenticate for me".
-- ---------------------------------------------------------------------------

CREATE OR REPLACE FUNCTION public.auth_user_by_email(p_email TEXT)
RETURNS TABLE (id TEXT, email TEXT, name TEXT, password_hash TEXT)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    SELECT u.id, u.email, u.name, u.password_hash
      FROM public.users u
     WHERE lower(u.email) = lower(btrim(p_email))
$$;

-- Memberships for the login tenant-picker. Called only after the password has
-- been verified, and only with a user id that came from auth_user_by_email --
-- never with a client-supplied id. It returns tenant names and roles, not
-- tenant content, so the blast radius of a caller bug is metadata.
CREATE OR REPLACE FUNCTION public.auth_memberships_for_user(p_user_id TEXT)
RETURNS TABLE (tenant_id TEXT, tenant_name TEXT, tenant_slug TEXT, role TEXT)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    SELECT t.id, t.name, t.slug, m.role
      FROM public.tenant_memberships m
      JOIN public.tenants t ON t.id = m.tenant_id
     WHERE m.user_id = p_user_id
     ORDER BY t.name
$$;

-- Session resolution. Keyed on the token digest, so it cannot be asked about a
-- user or tenant the caller did not already hold the credential for.
CREATE OR REPLACE FUNCTION public.auth_resolve_session(p_token_hash TEXT)
RETURNS TABLE (session_id TEXT, user_id TEXT, tenant_id TEXT, email TEXT, name TEXT, expires_at TIMESTAMPTZ)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    SELECT s.id, s.user_id, s.tenant_id, u.email, u.name, s.expires_at
      FROM public.sessions s
      JOIN public.users u ON u.id = s.user_id
     WHERE s.token_hash = p_token_hash
       AND s.expires_at > now()
$$;

-- Memberships for the tenant switcher, derived from the session token rather
-- than from a caller-supplied user id. Same reason as above: no parameter that
-- would let a bug ask "what does user X belong to".
CREATE OR REPLACE FUNCTION public.auth_session_memberships(p_token_hash TEXT)
RETURNS TABLE (tenant_id TEXT, tenant_name TEXT, tenant_slug TEXT, role TEXT)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    SELECT t.id, t.name, t.slug, m.role
      FROM public.sessions s
      JOIN public.tenant_memberships m ON m.user_id = s.user_id
      JOIN public.tenants t ON t.id = m.tenant_id
     WHERE s.token_hash = p_token_hash
       AND s.expires_at > now()
     ORDER BY t.name
$$;

-- Creates a tenant, its owner user and the owner membership atomically. There
-- is no state in which a tenant exists without an owner.
CREATE OR REPLACE FUNCTION public.auth_register_tenant(
    p_tenant_id TEXT, p_tenant_name TEXT, p_tenant_slug TEXT,
    p_user_id TEXT, p_email TEXT, p_password_hash TEXT, p_name TEXT,
    p_membership_id TEXT
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
    INSERT INTO public.tenants (id, name, slug)
    VALUES (p_tenant_id, p_tenant_name, p_tenant_slug);

    INSERT INTO public.users (id, email, password_hash, name)
    VALUES (p_user_id, lower(btrim(p_email)), p_password_hash, p_name);

    INSERT INTO public.tenant_memberships (id, tenant_id, user_id, role)
    VALUES (p_membership_id, p_tenant_id, p_user_id, 'owner');
END
$$;

-- Mints a session, but only for a tenant the user actually belongs to. Returns
-- false instead of raising, so the API can answer 403 rather than 500.
CREATE OR REPLACE FUNCTION public.auth_create_session(
    p_id TEXT, p_user_id TEXT, p_tenant_id TEXT, p_token_hash TEXT, p_expires_at TIMESTAMPTZ
) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM public.tenant_memberships m
         WHERE m.user_id = p_user_id AND m.tenant_id = p_tenant_id
    ) THEN
        RETURN false;
    END IF;

    INSERT INTO public.sessions (id, user_id, tenant_id, token_hash, expires_at)
    VALUES (p_id, p_user_id, p_tenant_id, p_token_hash, p_expires_at);

    RETURN true;
END
$$;

CREATE OR REPLACE FUNCTION public.auth_delete_session(p_token_hash TEXT)
RETURNS void
LANGUAGE sql SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    DELETE FROM public.sessions WHERE token_hash = p_token_hash
$$;

CREATE OR REPLACE FUNCTION public.auth_delete_user_sessions(p_user_id TEXT)
RETURNS void
LANGUAGE sql SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    DELETE FROM public.sessions WHERE user_id = p_user_id
$$;

-- ---------------------------------------------------------------------------
-- Privileges for the definer role.
--
-- BYPASSRLS skips policies; it does not grant table privileges. SECURITY DEFINER
-- runs with the *owner's* privileges, so aic_auth needs explicit grants on the
-- tables its functions touch. This is the one role that can read across tenants,
-- so it gets exactly what the functions need and nothing more: no UPDATE on
-- users, no DELETE on tenants.
-- ---------------------------------------------------------------------------
GRANT SELECT, INSERT             ON public.tenants            TO aic_auth;
GRANT SELECT, INSERT             ON public.users              TO aic_auth;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.tenant_memberships TO aic_auth;
GRANT SELECT, INSERT, DELETE     ON public.sessions           TO aic_auth;

-- ---------------------------------------------------------------------------
-- Ownership and privileges.
-- ---------------------------------------------------------------------------
ALTER FUNCTION public.auth_user_by_email(TEXT)            OWNER TO aic_auth;
ALTER FUNCTION public.auth_memberships_for_user(TEXT)     OWNER TO aic_auth;
ALTER FUNCTION public.auth_resolve_session(TEXT)          OWNER TO aic_auth;
ALTER FUNCTION public.auth_session_memberships(TEXT)      OWNER TO aic_auth;
ALTER FUNCTION public.auth_register_tenant(TEXT, TEXT, TEXT, TEXT, TEXT, TEXT, TEXT, TEXT) OWNER TO aic_auth;
ALTER FUNCTION public.auth_create_session(TEXT, TEXT, TEXT, TEXT, TIMESTAMPTZ) OWNER TO aic_auth;
ALTER FUNCTION public.auth_delete_session(TEXT)           OWNER TO aic_auth;
ALTER FUNCTION public.auth_delete_user_sessions(TEXT)     OWNER TO aic_auth;

-- Functions default to EXECUTE for PUBLIC, which would hand every role the
-- privileged surface. Revoke first, then grant to the one role that needs it.
REVOKE EXECUTE ON FUNCTION public.auth_user_by_email(TEXT)            FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.auth_memberships_for_user(TEXT)     FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.auth_resolve_session(TEXT)          FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.auth_session_memberships(TEXT)      FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.auth_register_tenant(TEXT, TEXT, TEXT, TEXT, TEXT, TEXT, TEXT, TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.auth_create_session(TEXT, TEXT, TEXT, TEXT, TIMESTAMPTZ) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.auth_delete_session(TEXT)           FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.auth_delete_user_sessions(TEXT)     FROM PUBLIC;

GRANT EXECUTE ON FUNCTION public.auth_user_by_email(TEXT)            TO aic_app;
GRANT EXECUTE ON FUNCTION public.auth_memberships_for_user(TEXT)     TO aic_app;
GRANT EXECUTE ON FUNCTION public.auth_resolve_session(TEXT)          TO aic_app;
GRANT EXECUTE ON FUNCTION public.auth_session_memberships(TEXT)      TO aic_app;
GRANT EXECUTE ON FUNCTION public.auth_register_tenant(TEXT, TEXT, TEXT, TEXT, TEXT, TEXT, TEXT, TEXT) TO aic_app;
GRANT EXECUTE ON FUNCTION public.auth_create_session(TEXT, TEXT, TEXT, TEXT, TIMESTAMPTZ) TO aic_app;
GRANT EXECUTE ON FUNCTION public.auth_delete_session(TEXT)           TO aic_app;
GRANT EXECUTE ON FUNCTION public.auth_delete_user_sessions(TEXT)     TO aic_app;

-- The app role keeps no direct access to the identity plane. With no table
-- privileges there is nothing for a forgotten predicate to leak: the only way in
-- is through a function that already knows which row it may touch.
REVOKE ALL ON public.tenants            FROM aic_app;
REVOKE ALL ON public.users              FROM aic_app;
REVOKE ALL ON public.tenant_memberships FROM aic_app;
REVOKE ALL ON public.sessions           FROM aic_app;

-- ---------------------------------------------------------------------------
-- Now FORCE can be turned on. The owner is bound by policy too, and the only
-- role that bypasses it is aic_auth, which cannot log in.
-- ---------------------------------------------------------------------------
ALTER TABLE public.tenant_memberships FORCE ROW LEVEL SECURITY;
ALTER TABLE public.sessions           FORCE ROW LEVEL SECURITY;
