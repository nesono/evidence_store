ALTER TABLE campaign_attempt
    ADD COLUMN finalized_at TIMESTAMPTZ,
    ADD COLUMN finished_at TIMESTAMPTZ,
    ADD COLUMN finalized_revision BIGINT,
    ADD COLUMN evidence_id UUID UNIQUE REFERENCES evidence(id) ON DELETE SET NULL;

-- A draft must not retain a second copy of evidence after its retention policy
-- deletes the record. Keep only identity/timestamps as an expired-result marker.
CREATE FUNCTION clear_expired_campaign_attempt() RETURNS TRIGGER AS $$
BEGIN
    UPDATE campaign_attempt SET data = '{}'::jsonb,
        last_operation_id = NULL, last_base_revision = NULL
    WHERE evidence_id = OLD.id;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER campaign_attempt_evidence_expiry
BEFORE DELETE ON evidence FOR EACH ROW EXECUTE FUNCTION clear_expired_campaign_attempt();
