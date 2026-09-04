DELETE FROM role_bindings WHERE role = 'provisioner';
ALTER TABLE role_bindings DROP CONSTRAINT role_bindings_role_check;
ALTER TABLE role_bindings ADD CONSTRAINT role_bindings_role_check
    CHECK (role IN ('viewer','contributor','ci','admin'));
