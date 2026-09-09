-- +goose Up
ALTER TABLE sources
    ADD COLUMN last_successful_sync_at DATETIME(6) NULL DEFAULT NULL
        AFTER config_json;

-- +goose Down
ALTER TABLE sources
    DROP COLUMN last_successful_sync_at;
