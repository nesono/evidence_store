DROP TRIGGER campaign_attempt_evidence_expiry ON evidence;
DROP FUNCTION clear_expired_campaign_attempt();
-- Older versions treat all attempt rows as editable drafts. Do not resurrect
-- finalized/expired results as editable work when rolling back.
DELETE FROM campaign_attempt WHERE finalized_at IS NOT NULL;
ALTER TABLE campaign_attempt
    DROP COLUMN evidence_id,
    DROP COLUMN finalized_revision,
    DROP COLUMN finished_at,
    DROP COLUMN finalized_at;
