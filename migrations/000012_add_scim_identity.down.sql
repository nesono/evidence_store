DROP INDEX IF EXISTS idx_principals_unclaimed_scim;
DROP INDEX IF EXISTS idx_principals_user_name;
DROP INDEX IF EXISTS idx_principals_scim_id;
ALTER TABLE principals DROP COLUMN user_name;
ALTER TABLE principals DROP COLUMN scim_external_id;
ALTER TABLE principals DROP COLUMN scim_id;
