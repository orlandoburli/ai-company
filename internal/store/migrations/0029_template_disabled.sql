ALTER TABLE workflow_templates ADD COLUMN disabled_at TIMESTAMPTZ;

COMMENT ON COLUMN workflow_templates.disabled_at IS
    'When set, the template is retired: its history is kept, it cannot run, and it may be deleted.';

DROP TRIGGER workflow_templates_published_no_mutation ON workflow_templates;

CREATE OR REPLACE FUNCTION public.workflow_templates_published_immutable()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
    IF OLD.published_at IS NOT NULL THEN
        IF TG_OP = 'DELETE' THEN
            IF OLD.disabled_at IS NOT NULL THEN
                RETURN OLD;
            END IF;
        ELSIF OLD.disabled_at IS DISTINCT FROM NEW.disabled_at
           AND (to_jsonb(NEW) - 'disabled_at') IS NOT DISTINCT FROM
               (to_jsonb(OLD) - 'disabled_at') THEN
            RETURN NEW;
        END IF;

        RAISE EXCEPTION 'published_workflow_template_is_immutable'
            USING ERRCODE = 'AIC07';
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER workflow_templates_published_no_mutation
    BEFORE UPDATE OR DELETE ON workflow_templates
    FOR EACH ROW EXECUTE FUNCTION public.workflow_templates_published_immutable();
