-- Dropping the reference index leaves the blobs themselves in the object store.
-- Without this table nothing knows which of them are still reachable, so the
-- sweep must be disabled before rolling back -- it would find no references and
-- consider every object garbage.
DROP TABLE IF EXISTS blob_ref;
