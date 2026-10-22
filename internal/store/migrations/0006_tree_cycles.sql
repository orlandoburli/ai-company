-- 0006_tree_cycles.sql — reject parent chains that would close a cycle.
--
-- 0004 only refused parent_id = id (CHECK on both tables) and left deeper loops
-- to the application. The API's depth rule does not see them: moving X under
-- its own descendant passes the depth arithmetic (the moved subtree's height is
-- small enough even though the result is no longer a tree). Both holes — items
-- and projects — close here at the database level, with the same shape the
-- dependency-edge trigger in 0004 uses: walk the existing ancestor chain from
-- the new parent and refuse the write when it reaches the row's own id,
-- under SQLSTATE AIC04, which internal/work maps to 422 parent_cycle.
--
-- The function is deliberately not SECURITY DEFINER: the walk runs with the
-- caller's privileges and therefore through row-level security, never around
-- it. Tenant scoping rides the table's composite pattern — every walk step is
-- filtered on tenant_id = NEW.tenant_id, so the walk stays inside the row's
-- tenant even under concurrent writes.
--
-- Nothing here rewrites data; on an already-valid tree every statement is a
-- no-op check, so the migration is safe to apply against existing rows.

CREATE OR REPLACE FUNCTION public.reject_tree_cycle() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    cycle BOOLEAN;
BEGIN
    IF NEW.parent_id IS NULL THEN
        RETURN NEW;
    END IF;

    -- parent_id = id is already forbidden by the table CHECK, but report it
    -- here with the same shape so the API has one error to map.
    IF NEW.parent_id = NEW.id THEN
        RAISE EXCEPTION 'tree_cycle' USING ERRCODE = 'AIC04';
    END IF;

    -- The table is resolved from the trigger context, so one function serves
    -- items and projects. The walk starts at the new parent and climbs its
    -- existing ancestor chain; NEW is not in the table yet, so reaching NEW.id
    -- means the chain comes back around to the row being written.
    --
    -- Bounded at 64 climbs (the product rule is 6), so even corrupt legacy
    -- data with an existing loop terminates instead of spinning.
    EXECUTE format(
        'WITH RECURSIVE chain(id, depth) AS (
              SELECT $2, 1
              UNION
              SELECT p.parent_id, c.depth + 1
                FROM %I.%I p
                JOIN chain c ON p.id = c.id
               WHERE p.tenant_id = $3
                 AND p.parent_id IS NOT NULL
                 AND c.depth < 64
          )
          SELECT EXISTS (SELECT 1 FROM chain WHERE id = $1)',
        TG_TABLE_SCHEMA, TG_TABLE_NAME
    ) INTO cycle
    USING NEW.id, NEW.parent_id, NEW.tenant_id;

    IF cycle THEN
        RAISE EXCEPTION 'tree_cycle' USING ERRCODE = 'AIC04';
    END IF;

    RETURN NEW;
END
$$;

CREATE TRIGGER items_no_parent_cycles
    BEFORE INSERT OR UPDATE OF parent_id ON items
    FOR EACH ROW EXECUTE FUNCTION public.reject_tree_cycle();

CREATE TRIGGER projects_no_parent_cycles
    BEFORE INSERT OR UPDATE OF parent_id ON projects
    FOR EACH ROW EXECUTE FUNCTION public.reject_tree_cycle();
