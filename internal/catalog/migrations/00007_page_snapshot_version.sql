-- +goose Up

-- A rolling deployment can run the old extractor next to the new one. The old worker does not know about the version
-- fence in savePageContent, so a database trigger is the check both versions run: current_snapshot_id cannot move to a
-- lower extractor version. That keeps the current text, official evidence and any open review from flapping between
-- extractor versions while old workers drain. Rolling back to an older extractor needs an explicit migration.
-- +goose StatementBegin
CREATE FUNCTION catalog.refuse_page_extractor_downgrade() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
DECLARE
    old_version integer;
    new_version integer;
BEGIN
    IF NEW.current_snapshot_id IS NOT DISTINCT FROM OLD.current_snapshot_id THEN
        RETURN NEW;
    END IF;
    IF OLD.current_snapshot_id IS NOT NULL THEN
        SELECT extractor_version INTO old_version
        FROM catalog.page_snapshot WHERE id = OLD.current_snapshot_id;
    END IF;
    SELECT extractor_version INTO new_version
    FROM catalog.page_snapshot WHERE id = NEW.current_snapshot_id;
    IF old_version IS NOT NULL AND new_version < old_version THEN
        RAISE EXCEPTION 'page snapshot extractor version cannot go backwards: % -> %', old_version, new_version
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER refuse_page_extractor_downgrade
    BEFORE UPDATE OF current_snapshot_id ON catalog.watched_page
    FOR EACH ROW EXECUTE FUNCTION catalog.refuse_page_extractor_downgrade();
