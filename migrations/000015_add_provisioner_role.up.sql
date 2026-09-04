-- A role for the directory's own token.
--
-- Provisioning has been guarded by principal:admin since it was built, which
-- made the credential a directory holds an administrator of this store: able to
-- grant roles by hand, declare inheritance, and read every record. A token that
-- lives in another company's configuration for years should be able to do
-- exactly one thing.
--
-- It is also what makes the last-administrator guard mean anything. While the
-- caller had to be an admin, deactivating the last one was impossible by
-- construction -- the caller was always another -- so the check could never
-- fire on the only path that can reach it.
ALTER TABLE role_bindings DROP CONSTRAINT role_bindings_role_check;
ALTER TABLE role_bindings ADD CONSTRAINT role_bindings_role_check
    CHECK (role IN ('viewer','contributor','ci','admin','provisioner'));
