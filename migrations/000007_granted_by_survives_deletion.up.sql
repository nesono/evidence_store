-- Let a principal be deleted without taking the grants it issued with it.
--
-- role_bindings.granted_by references principals(id) with no ON DELETE clause,
-- which defaults to NO ACTION: deleting an administrator fails if they ever
-- granted a role to anybody. That is not a hypothetical. It is what happens the
-- first time an operator clears out a test principal by hand, and the error
-- names a constraint rather than the administrator who is in the way.
--
-- SET NULL rather than CASCADE. The grant is a fact about the principal that
-- holds it; who issued it is provenance, and provenance going missing is a
-- lesser loss than a role silently disappearing from someone's account because
-- the colleague who granted it left. The column is already nullable, for the
-- bootstrap admin the server grants on its own authority.
--
-- The store still prefers disabling to deleting, and the API offers no delete
-- at all -- revocation is a timestamp so that evidence already attributed to a
-- principal still names something. This is about the deletions that happen
-- anyway, in psql.
ALTER TABLE role_bindings
    DROP CONSTRAINT role_bindings_granted_by_fkey;

ALTER TABLE role_bindings
    ADD CONSTRAINT role_bindings_granted_by_fkey
    FOREIGN KEY (granted_by) REFERENCES principals(id) ON DELETE SET NULL;
