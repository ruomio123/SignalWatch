-- +goose Up
CREATE TABLE subscription_backfills (
 subscription_id BIGINT UNSIGNED PRIMARY KEY,
 source_id BIGINT UNSIGNED NOT NULL,
 category VARCHAR(64) NOT NULL,
 keywords_json JSON NOT NULL,
 window_from DATETIME(6) NOT NULL,
 window_to DATETIME(6) NOT NULL,
 cursor_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
 state VARCHAR(16) NOT NULL DEFAULT 'pending',
 processed BIGINT UNSIGNED NOT NULL DEFAULT 0,
 matched BIGINT UNSIGNED NOT NULL DEFAULT 0,
 attempts INT NOT NULL DEFAULT 0,
 lease_owner VARCHAR(64) NOT NULL DEFAULT '',
 lease_until DATETIME(6) NULL,
 next_retry_at DATETIME(6) NOT NULL,
 failure_code VARCHAR(64) NOT NULL DEFAULT '',
 updated_at DATETIME(6) NOT NULL,
 INDEX idx_backfill_pending(state,next_retry_at,subscription_id),
 CONSTRAINT fk_backfill_subscription FOREIGN KEY(subscription_id) REFERENCES subscriptions(id) ON DELETE CASCADE,
 CONSTRAINT chk_backfill_state CHECK(state IN ('pending','processing','complete','failed','cancelled'))
) ENGINE=InnoDB;

-- +goose Down
DROP TABLE subscription_backfills;
