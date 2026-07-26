-- branch is filterable via the API and sortable from the UI, but was the only
-- such column without an index.
CREATE INDEX idx_evidence_branch ON evidence (branch);
