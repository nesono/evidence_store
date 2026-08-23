-- Dropping source collapses IdP-derived grants into ordinary ones rather than
-- deleting them: a rollback should not take away access people are using. The
-- next login on a re-upgraded store reconciles them back into shape.
ALTER TABLE role_bindings DROP COLUMN source;

DROP INDEX IF EXISTS idx_principals_external_id;
ALTER TABLE principals DROP COLUMN external_id;

DROP TABLE IF EXISTS sessions;
