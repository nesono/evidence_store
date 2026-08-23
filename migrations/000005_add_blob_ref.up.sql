-- Which evidence records reference which blobs.
--
-- Blobs are content-addressed and deduplicated, so a blob has no owner: the
-- same screenshot pasted into two records is one object that both point at.
-- Deletion is therefore reachability, not ownership -- the sweep in
-- internal/retention deletes objects no row here names.
--
-- The alternative was deriving the reachable set from metadata on every sweep
-- (SELECT DISTINCT jsonb_array_elements_text(metadata->'photo_uris') ...). That
-- needs no table, but it is a full scan of the evidence table each pass, which
-- is the wrong shape for a store expected to hold a lot of rows. This table is
-- written once per referenced blob per record and turns the sweep into an
-- index-only anti-join.
--
-- ON DELETE CASCADE is what makes retention deleting a record release its
-- blobs: the refs go with the row, and the sweep picks the objects up on its
-- next pass.
CREATE TABLE blob_ref (
    digest      TEXT NOT NULL,
    evidence_id UUID NOT NULL REFERENCES evidence(id) ON DELETE CASCADE,
    PRIMARY KEY (digest, evidence_id)
);

-- The primary key already indexes digest first, which serves the sweep's
-- lookup. This index serves the other direction: everything a record points at.
CREATE INDEX idx_blob_ref_evidence ON blob_ref (evidence_id);
