-- 0019_github_links.sql — tenant-scoped mappings between GitHub issues and items.
CREATE TABLE github_issue_links (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    product_id     TEXT NOT NULL,
    repo           TEXT NOT NULL,
    issue_number   INT NOT NULL CHECK (issue_number > 0),
    item_id        TEXT,
    issue_state    TEXT NOT NULL CHECK (issue_state IN ('open', 'closed')),
    issue_title    TEXT NOT NULL DEFAULT '',
    issue_url      TEXT NOT NULL DEFAULT '',
    last_synced_at TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    FOREIGN KEY (product_id, tenant_id) REFERENCES products (id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (item_id, tenant_id) REFERENCES items (id, tenant_id) ON DELETE SET NULL (item_id),
    UNIQUE (tenant_id, repo, issue_number),
    UNIQUE (id, tenant_id)
);

CREATE INDEX github_issue_links_tenant_product_idx
    ON github_issue_links (tenant_id, product_id, issue_number);
CREATE INDEX github_issue_links_tenant_item_idx
    ON github_issue_links (tenant_id, item_id) WHERE item_id IS NOT NULL;

ALTER TABLE github_issue_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE github_issue_links FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON github_issue_links
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON github_issue_links TO aic_app;
