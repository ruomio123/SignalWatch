-- +goose Up
ALTER TABLE subscriptions
 ADD COLUMN max_items_per_digest SMALLINT UNSIGNED NOT NULL DEFAULT 50,
 ADD CONSTRAINT chk_subscription_digest_limit CHECK (max_items_per_digest BETWEEN 1 AND 50);
UPDATE subscriptions s JOIN users u ON u.id=s.user_id
 SET s.max_items_per_digest=u.max_items_per_digest;

-- +goose Down
ALTER TABLE subscriptions DROP CHECK chk_subscription_digest_limit,
 DROP COLUMN max_items_per_digest;
