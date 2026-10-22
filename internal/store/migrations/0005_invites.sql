-- 0005_invites.sql — single-use, expiring invitations (FR-TN-7).
--
-- An invitation creates a *membership*, which is identity-plane data, so it
-- follows the same rule as 0002/0003: this table has no grants for aic_app and is
-- reached only through SECURITY DEFINER functions. Acceptance is unauthenticated
-- by nature — the invite token is the credential — so it is keyed on the token
-- digest and never on a caller-supplied tenant or user id.
--
-- Two properties are enforced here rather than in Go:
--   * an invitation can never grant `owner` (CHECK), so inviting is not a way to
--     mint a peer of the inviter's privilege level;
--   * at most one pending invite per (tenant, email) — a second invite is a
--     refusal, not a duplicate row that acceptance would race on.

CREATE TABLE tenant_invites (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    email       TEXT        NOT NULL,
    role        TEXT        NOT NULL CHECK (role IN ('admin', 'member', 'guest')),
    token_hash  TEXT        NOT NULL UNIQUE,
    invited_by  TEXT        REFERENCES users (id) ON DELETE SET NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    accepted_at TIMESTAMPTZ,
    accepted_by TEXT        REFERENCES users (id) ON DELETE SET NULL,
    revoked_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX tenant_invites_pending_email
    ON tenant_invites (tenant_id, lower(email))
    WHERE accepted_at IS NULL AND revoked_at IS NULL;

CREATE INDEX tenant_invites_tenant_idx ON tenant_invites (tenant_id, created_at DESC);

ALTER TABLE tenant_invites ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_invites FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_invites
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());

-- The app role gets nothing directly: default privileges would otherwise hand it
-- SELECT/INSERT/UPDATE/DELETE like any content table.
REVOKE ALL ON tenant_invites FROM aic_app;
GRANT SELECT, INSERT, UPDATE ON tenant_invites TO aic_auth;

-- ---------------------------------------------------------------------------
-- Create an invitation. Returns NULL on success, or a coarse reason.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.auth_invite_create(
    p_token_hash TEXT, p_invite_id TEXT, p_email TEXT, p_role TEXT,
    p_invite_token_hash TEXT, p_expires_at TIMESTAMPTZ
) RETURNS TEXT
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    v_role   TEXT;
    v_tenant TEXT;
    v_user   TEXT;
    v_email  TEXT;
BEGIN
    SELECT auth_session_role(p_token_hash) INTO v_role;
    IF v_role IS NULL THEN
        RETURN 'unauthenticated';
    END IF;
    IF v_role NOT IN ('owner', 'admin') THEN
        RETURN 'forbidden';
    END IF;
    IF p_role NOT IN ('admin', 'member', 'guest') THEN
        RETURN 'invalid_role';
    END IF;

    SELECT s.tenant_id, s.user_id INTO v_tenant, v_user
      FROM public.sessions s
     WHERE s.token_hash = p_token_hash AND s.expires_at > now();

    v_email := lower(btrim(p_email));

    IF EXISTS (
        SELECT 1
          FROM public.tenant_memberships m
          JOIN public.users u ON u.id = m.user_id
         WHERE m.tenant_id = v_tenant AND lower(u.email) = v_email
    ) THEN
        RETURN 'already_member';
    END IF;

    BEGIN
        INSERT INTO public.tenant_invites
            (id, tenant_id, email, role, token_hash, invited_by, expires_at)
        VALUES (p_invite_id, v_tenant, v_email, p_role, p_invite_token_hash, v_user, p_expires_at);
    EXCEPTION WHEN unique_violation THEN
        RETURN 'already_invited';
    END;

    RETURN NULL;
END
$$;

-- ---------------------------------------------------------------------------
-- Pending invitations for the session's tenant. Non-admins see nothing rather
-- than an error: the list is admin tooling, not tenant content.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.auth_invite_list(p_token_hash TEXT)
RETURNS TABLE (id TEXT, email TEXT, role TEXT, expires_at TIMESTAMPTZ, created_at TIMESTAMPTZ)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    v_role   TEXT;
    v_tenant TEXT;
BEGIN
    SELECT auth_session_role(p_token_hash) INTO v_role;
    IF v_role IS NULL OR v_role NOT IN ('owner', 'admin') THEN
        RETURN;
    END IF;

    SELECT s.tenant_id INTO v_tenant
      FROM public.sessions s
     WHERE s.token_hash = p_token_hash AND s.expires_at > now();

    RETURN QUERY
        SELECT i.id, i.email, i.role, i.expires_at, i.created_at
          FROM public.tenant_invites i
         WHERE i.tenant_id = v_tenant
           AND i.accepted_at IS NULL
           AND i.revoked_at IS NULL
         ORDER BY i.created_at DESC;
END
$$;

-- ---------------------------------------------------------------------------
-- Revoke a pending invitation.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.auth_invite_revoke(
    p_token_hash TEXT, p_invite_id TEXT
) RETURNS TEXT
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    v_role   TEXT;
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

    UPDATE public.tenant_invites
       SET revoked_at = now()
     WHERE id = p_invite_id
       AND tenant_id = v_tenant
       AND accepted_at IS NULL
       AND revoked_at IS NULL;

    IF NOT FOUND THEN
        RETURN 'not_found';
    END IF;
    RETURN NULL;
END
$$;

-- ---------------------------------------------------------------------------
-- Peek at an invitation before signing up, so the accept page can show the
-- tenant and invitee without consuming the token.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.auth_invite_peek(p_invite_token_hash TEXT)
RETURNS TABLE (tenant_name TEXT, email TEXT, role TEXT, expires_at TIMESTAMPTZ)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    SELECT t.name, i.email, i.role, i.expires_at
      FROM public.tenant_invites i
      JOIN public.tenants t ON t.id = i.tenant_id
     WHERE i.token_hash = p_invite_token_hash
       AND i.accepted_at IS NULL
       AND i.revoked_at IS NULL
       AND i.expires_at > now()
$$;

-- ---------------------------------------------------------------------------
-- Accept an invitation.
--
-- The token is the credential, so this is the only unauthenticated function that
-- writes a membership. It creates the user when the email is new and reuses the
-- existing account when it is not — but it never changes an existing password,
-- so a leaked invite cannot take over an account that already exists.
--
-- Returns no rows when the token is unknown, expired, revoked or already used;
-- the caller cannot tell those cases apart, which is intentional.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.auth_invite_accept(
    p_invite_token_hash TEXT, p_user_id TEXT, p_password_hash TEXT,
    p_name TEXT, p_membership_id TEXT
) RETURNS TABLE (tenant_id TEXT, user_id TEXT, email TEXT, role TEXT)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    v_invite   public.tenant_invites%ROWTYPE;
    v_user     TEXT;
    v_role     TEXT;
BEGIN
    SELECT * INTO v_invite
      FROM public.tenant_invites
     WHERE token_hash = p_invite_token_hash
       AND accepted_at IS NULL
       AND revoked_at IS NULL
       AND expires_at > now()
       FOR UPDATE;

    IF NOT FOUND THEN
        RETURN;
    END IF;

    SELECT u.id INTO v_user
      FROM public.users u
     WHERE lower(u.email) = lower(v_invite.email);

    IF v_user IS NULL THEN
        v_user := p_user_id;
        INSERT INTO public.users (id, email, password_hash, name)
        VALUES (v_user, v_invite.email, p_password_hash, btrim(p_name));
    END IF;

    SELECT m.role INTO v_role
      FROM public.tenant_memberships m
     WHERE m.tenant_id = v_invite.tenant_id AND m.user_id = v_user;

    IF v_role IS NULL THEN
        INSERT INTO public.tenant_memberships (id, tenant_id, user_id, role)
        VALUES (p_membership_id, v_invite.tenant_id, v_user, v_invite.role);
        v_role := v_invite.role;
    END IF;

    UPDATE public.tenant_invites
       SET accepted_at = now(), accepted_by = v_user
     WHERE id = v_invite.id;

    RETURN QUERY SELECT v_invite.tenant_id, v_user, v_invite.email, v_role;
END
$$;

-- Ownership and privileges, same pattern as 0002.
ALTER FUNCTION public.auth_invite_create(TEXT, TEXT, TEXT, TEXT, TEXT, TIMESTAMPTZ) OWNER TO aic_auth;
ALTER FUNCTION public.auth_invite_list(TEXT)       OWNER TO aic_auth;
ALTER FUNCTION public.auth_invite_revoke(TEXT, TEXT) OWNER TO aic_auth;
ALTER FUNCTION public.auth_invite_peek(TEXT)       OWNER TO aic_auth;
ALTER FUNCTION public.auth_invite_accept(TEXT, TEXT, TEXT, TEXT, TEXT) OWNER TO aic_auth;

REVOKE EXECUTE ON FUNCTION public.auth_invite_create(TEXT, TEXT, TEXT, TEXT, TEXT, TIMESTAMPTZ) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.auth_invite_list(TEXT)       FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.auth_invite_revoke(TEXT, TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.auth_invite_peek(TEXT)       FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.auth_invite_accept(TEXT, TEXT, TEXT, TEXT, TEXT) FROM PUBLIC;

GRANT EXECUTE ON FUNCTION public.auth_invite_create(TEXT, TEXT, TEXT, TEXT, TEXT, TIMESTAMPTZ) TO aic_app;
GRANT EXECUTE ON FUNCTION public.auth_invite_list(TEXT)       TO aic_app;
GRANT EXECUTE ON FUNCTION public.auth_invite_revoke(TEXT, TEXT) TO aic_app;
GRANT EXECUTE ON FUNCTION public.auth_invite_peek(TEXT)       TO aic_app;
GRANT EXECUTE ON FUNCTION public.auth_invite_accept(TEXT, TEXT, TEXT, TEXT, TEXT) TO aic_app;
