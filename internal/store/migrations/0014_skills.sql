-- 0014_skills.sql — tenant/product scoped, versioned operator procedures.
-- Approved content is immutable; AIC08 is the next free application SQLSTATE.

CREATE TABLE skills (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    product_id       TEXT,
    name             TEXT        NOT NULL,
    slug             TEXT        NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    description      TEXT        NOT NULL DEFAULT '',
    current_version  INT         NOT NULL DEFAULT 0 CHECK (current_version >= 0),
    enabled          BOOLEAN     NOT NULL DEFAULT TRUE,
    created_by       TEXT        REFERENCES users (id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    FOREIGN KEY (product_id, tenant_id) REFERENCES products (id, tenant_id) ON DELETE CASCADE,
    UNIQUE (tenant_id, product_id, slug),
    UNIQUE (id, tenant_id)
);

-- PostgreSQL treats NULLs as distinct in UNIQUE constraints, so this partial
-- index completes the tenant-wide slug uniqueness promised by the table shape.
CREATE UNIQUE INDEX skills_tenant_slug_uniq ON skills (tenant_id, slug) WHERE product_id IS NULL;
CREATE INDEX skills_tenant_idx ON skills (tenant_id, product_id, slug);

CREATE TABLE skill_versions (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    skill_id    TEXT        NOT NULL,
    version     INT         NOT NULL CHECK (version >= 1),
    content_md  TEXT        NOT NULL,
    content_sha TEXT        NOT NULL CHECK (content_sha ~ '^[0-9a-f]{64}$'),
    author_kind TEXT        NOT NULL CHECK (author_kind IN ('human', 'agent_draft')),
    author_id   TEXT,
    approved_by TEXT        REFERENCES users (id) ON DELETE SET NULL,
    approved_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    FOREIGN KEY (skill_id, tenant_id) REFERENCES skills (id, tenant_id) ON DELETE CASCADE,
    CHECK ((approved_by IS NULL) = (approved_at IS NULL)),
    CHECK (author_kind <> 'human' OR approved_by IS NOT NULL),
    UNIQUE (skill_id, version),
    UNIQUE (id, tenant_id)
);

CREATE INDEX skill_versions_skill_idx ON skill_versions (tenant_id, skill_id, version DESC);

CREATE OR REPLACE FUNCTION public.skill_versions_approved_content_immutable()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
    IF OLD.approved_at IS NOT NULL
       AND (NEW.content_md IS DISTINCT FROM OLD.content_md
            OR NEW.content_sha IS DISTINCT FROM OLD.content_sha) THEN
        RAISE EXCEPTION 'approved_skill_version_content_is_immutable'
            USING ERRCODE = 'AIC08';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER skill_versions_approved_content_guard
    BEFORE UPDATE ON skill_versions
    FOR EACH ROW EXECUTE FUNCTION public.skill_versions_approved_content_immutable();

ALTER TABLE skills ENABLE ROW LEVEL SECURITY;
ALTER TABLE skill_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE skills FORCE ROW LEVEL SECURITY;
ALTER TABLE skill_versions FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON skills
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON skill_versions
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON skills TO aic_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON skill_versions TO aic_app;
