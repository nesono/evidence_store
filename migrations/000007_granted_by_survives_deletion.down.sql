-- Back to blocking the delete. Rolling this back cannot restore the grantors
-- that a delete has already set to NULL -- that information is gone -- but the
-- constraint shape is what the schema version describes, so it is what comes
-- back.
ALTER TABLE role_bindings
    DROP CONSTRAINT role_bindings_granted_by_fkey;

ALTER TABLE role_bindings
    ADD CONSTRAINT role_bindings_granted_by_fkey
    FOREIGN KEY (granted_by) REFERENCES principals(id);
