-- +goose Up
ALTER TABLE subscriptions
    ADD COLUMN source_id BIGINT UNSIGNED NULL AFTER user_id,
    ADD COLUMN objective VARCHAR(500) NULL AFTER name,
    ADD COLUMN version INT UNSIGNED NOT NULL DEFAULT 1 AFTER enabled;

UPDATE subscriptions
INNER JOIN sources ON sources.source_key = 'arxiv'
SET subscriptions.source_id = sources.id
WHERE subscriptions.source_id IS NULL;

ALTER TABLE subscriptions
    MODIFY COLUMN source_id BIGINT UNSIGNED NOT NULL,
    ADD INDEX idx_subscriptions_source_id (source_id),
    ADD CONSTRAINT chk_subscriptions_version CHECK (version > 0),
    ADD CONSTRAINT fk_subscriptions_source
        FOREIGN KEY (source_id)
        REFERENCES sources(id)
        ON UPDATE RESTRICT
        ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE subscriptions
    DROP FOREIGN KEY fk_subscriptions_source,
    DROP CHECK chk_subscriptions_version,
    DROP INDEX idx_subscriptions_source_id,
    DROP COLUMN source_id,
    DROP COLUMN objective,
    DROP COLUMN version;
