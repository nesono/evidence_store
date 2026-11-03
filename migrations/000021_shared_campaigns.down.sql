DROP TABLE campaign_execution_event;
DROP INDEX campaign_attempt_one_shared_draft;
ALTER TABLE campaign_attempt DROP CONSTRAINT campaign_attempt_execution_package;
ALTER TABLE campaign_attempt DROP COLUMN execution_id;
ALTER TABLE campaign_attempt DROP COLUMN updated_by;
ALTER TABLE campaign_attempt DROP COLUMN finalized_by;
DROP TABLE campaign_execution;
