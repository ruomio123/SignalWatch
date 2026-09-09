-- +goose Up
ALTER TABLE users
    ADD COLUMN role VARCHAR(16) NOT NULL DEFAULT 'user' AFTER status,
    ADD CONSTRAINT chk_users_role CHECK (role IN ('user', 'operator')),
    ADD INDEX idx_users_role_status (role, status);

-- +goose Down
ALTER TABLE users
    DROP INDEX idx_users_role_status,
    DROP CHECK chk_users_role,
    DROP COLUMN role;
