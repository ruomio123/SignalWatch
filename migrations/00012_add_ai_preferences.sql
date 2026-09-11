-- +goose Up
ALTER TABLE users
 ADD COLUMN ai_enabled BOOLEAN NOT NULL DEFAULT FALSE,
 ADD COLUMN ai_language VARCHAR(4) NOT NULL DEFAULT 'zh',
 ADD COLUMN digest_ai_enabled BOOLEAN NOT NULL DEFAULT FALSE,
 ADD CONSTRAINT chk_users_ai_language CHECK (ai_language IN ('zh','en','both'));
-- +goose Down
ALTER TABLE users DROP CHECK chk_users_ai_language, DROP COLUMN digest_ai_enabled, DROP COLUMN ai_language, DROP COLUMN ai_enabled;
