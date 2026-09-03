-- When this identity last changed.
--
-- SCIM returns meta.lastModified on every resource, and a provisioner is
-- entitled to a truthful one: it is how a directory reasons about whether what
-- it sent has landed. created_at cannot stand in for it -- the interesting
-- moment is the deactivation, which happens long after the row was written.
ALTER TABLE principals ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
