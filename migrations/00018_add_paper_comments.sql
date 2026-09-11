-- +goose Up
ALTER TABLE papers ADD COLUMN comments TEXT NULL AFTER abstract;
UPDATE papers SET comments='' WHERE comments IS NULL;
ALTER TABLE papers MODIFY COLUMN comments TEXT NOT NULL;

-- +goose Down
ALTER TABLE papers DROP COLUMN comments;
