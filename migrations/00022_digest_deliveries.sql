-- +goose Up
CREATE TABLE digest_deliveries (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 user_id BIGINT UNSIGNED NOT NULL,
 subscription_id BIGINT UNSIGNED NOT NULL,
 local_date VARCHAR(10) NOT NULL,
 state VARCHAR(16) NOT NULL DEFAULT 'pending',
 snapshot_json JSON NULL,
 attempts INT NOT NULL DEFAULT 0,
 lease_owner VARCHAR(64) NOT NULL DEFAULT '',
 lease_until DATETIME(6) NULL,
 next_retry_at DATETIME(6) NOT NULL,
 failure_code VARCHAR(64) NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 UNIQUE KEY uk_digest_day(subscription_id,local_date),
 INDEX idx_delivery_retry(state,next_retry_at,id),
 CONSTRAINT fk_delivery_subscription FOREIGN KEY(subscription_id) REFERENCES subscriptions(id) ON DELETE CASCADE,
 CONSTRAINT fk_delivery_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
 CONSTRAINT chk_delivery_state CHECK(state IN ('pending','sending','retry','sent','empty','failed','cancelled'))
) ENGINE=InnoDB;
CREATE TABLE digest_delivery_items (
 subscription_id BIGINT UNSIGNED NOT NULL,
 paper_id BIGINT UNSIGNED NOT NULL,
 delivery_id BIGINT UNSIGNED NOT NULL,
 PRIMARY KEY(subscription_id,paper_id),
 INDEX idx_delivery_items(delivery_id),
 CONSTRAINT fk_delivery_item_relation FOREIGN KEY(subscription_id,paper_id) REFERENCES subscription_papers(subscription_id,paper_id) ON DELETE CASCADE,
 CONSTRAINT fk_delivery_item_delivery FOREIGN KEY(delivery_id) REFERENCES digest_deliveries(id) ON DELETE CASCADE
) ENGINE=InnoDB;

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='00022 contains delivery ownership; restore a verified backup';
