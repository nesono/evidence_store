-- A token the client chooses, so that sending the same submission twice files
-- one record.
--
-- The failure this is for is not a post that failed -- the client simply sends
-- that one again. It is the post that succeeded while the response was lost: a
-- dropped tunnel, a closed lid, a timeout on the far side. The store has the
-- record; the client cannot tell. Sending again files the event twice, and
-- afterwards the two rows differ only in id and ingested_at, which is
-- indistinguishable from a tester who ran the procedure twice and passed twice.
-- Nothing in the data can separate them, so the client has to say.
--
-- Offline collection (docs/offline-support-plan.md) makes that the expected
-- case rather than the rare one: a queue drains over whatever link a campaign
-- can find, and a batch that loses its response leaves every record in it in
-- doubt at once.
ALTER TABLE evidence ADD COLUMN client_record_id UUID;

-- Partial, so the column stays free for every client that does not send one --
-- which is all of them today. NULLs never conflict in a unique index anyway;
-- the predicate is what lets ON CONFLICT infer this index by name.
CREATE UNIQUE INDEX idx_evidence_client_record_id
    ON evidence (client_record_id) WHERE client_record_id IS NOT NULL;
