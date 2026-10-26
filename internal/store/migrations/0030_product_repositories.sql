-- Products may be connected to several GitHub repositories. repo_url remains
-- on products as a compatibility mirror of the default remote for existing
-- callers; product_repositories is the source of truth from this migration on.
CREATE TABLE product_repositories (
    id          TEXT        PRIMARY KEY,
    tenant_id   TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    product_id  TEXT        NOT NULL,
    repo        TEXT        NOT NULL CHECK (repo ~ '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$'),
    name        TEXT        CHECK (name IS NULL OR length(btrim(name)) BETWEEN 1 AND 80),
    is_default  BOOLEAN     NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    FOREIGN KEY (product_id, tenant_id) REFERENCES products (id, tenant_id) ON DELETE CASCADE,
    UNIQUE (tenant_id, product_id, repo),
    UNIQUE (id, tenant_id)
);

CREATE INDEX product_repositories_tenant_product_idx
    ON product_repositories (tenant_id, product_id, created_at);
CREATE UNIQUE INDEX product_repositories_one_default_idx
    ON product_repositories (tenant_id, product_id) WHERE is_default;

COMMENT ON TABLE product_repositories IS
    'GitHub repositories connected to a product. products.repo_url remains a compatibility mirror of the default remote and is not the source of truth.';
COMMENT ON COLUMN product_repositories.repo IS
    'GitHub repository in owner/name form, matching integration node configuration.';
COMMENT ON COLUMN product_repositories.name IS
    'Optional human-facing label for this repository.';
COMMENT ON COLUMN product_repositories.is_default IS
    'Whether this is the product default repository; at most one row per product is default.';

-- Migrate every legacy remote to a canonical owner/name row and keep it as the
-- product default. Existing GitHub remotes may be HTTPS, SSH, or owner/name.
INSERT INTO product_repositories (id, tenant_id, product_id, repo, is_default)
SELECT upper(md5(random()::text || clock_timestamp()::text)),
       p.tenant_id,
       p.id,
       lower(regexp_replace(
           regexp_replace(btrim(p.repo_url), '^.*github\.com[:/]', '', 'i'),
           '\.git/?$', '', 'i')),
       true
  FROM products p
 WHERE nullif(btrim(p.repo_url), '') IS NOT NULL;

ALTER TABLE product_repositories ENABLE ROW LEVEL SECURITY;
ALTER TABLE product_repositories FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON product_repositories
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON product_repositories TO aic_app;
