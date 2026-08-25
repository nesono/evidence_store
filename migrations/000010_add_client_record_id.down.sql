-- Dropping the column loses the tokens, and a client that had been relying on
-- them starts filing duplicates again on a lost response. The records
-- themselves are untouched: the token was never part of what a record says
-- about a test, only of how it got here.
DROP INDEX IF EXISTS idx_evidence_client_record_id;
ALTER TABLE evidence DROP COLUMN IF EXISTS client_record_id;
