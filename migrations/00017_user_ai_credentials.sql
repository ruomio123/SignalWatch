-- +goose Up
CREATE TABLE user_ai_configurations (
 user_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
 provider_id VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 model_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'active',
 config_version BIGINT UNSIGNED NOT NULL DEFAULT 1,
 key_hint VARCHAR(4) NOT NULL,
 secret_ciphertext VARBINARY(2048) NOT NULL,
 secret_nonce BINARY(12) NOT NULL,
 master_key_version VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 last_tested_at DATETIME(6) NULL,
 last_used_at DATETIME(6) NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 CONSTRAINT fk_user_ai_configurations_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
 CONSTRAINT chk_user_ai_configurations_status CHECK(status IN ('active','invalid'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE ai_user_daily_usage (
 user_id BIGINT UNSIGNED NOT NULL,
 day DATE NOT NULL,
 feature VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 calls INT UNSIGNED NOT NULL DEFAULT 0,
 succeeded INT UNSIGNED NOT NULL DEFAULT 0,
 failed INT UNSIGNED NOT NULL DEFAULT 0,
 input_tokens BIGINT UNSIGNED NOT NULL DEFAULT 0,
 output_tokens BIGINT UNSIGNED NOT NULL DEFAULT 0,
 usage_missing INT UNSIGNED NOT NULL DEFAULT 0,
 PRIMARY KEY(user_id,day,feature),
 CONSTRAINT fk_ai_user_daily_usage_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
 CONSTRAINT chk_ai_user_daily_usage_feature CHECK(feature IN ('digest','paper','config_test'))
) ENGINE=InnoDB;

CREATE TABLE ai_user_call_leases (
 user_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
 lease_token VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 lease_until DATETIME(6) NOT NULL,
 CONSTRAINT fk_ai_user_call_leases_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB;

ALTER TABLE subscriptions DROP CHECK chk_subscription_digest_limit;
ALTER TABLE subscriptions
 MODIFY COLUMN max_items_per_digest SMALLINT UNSIGNED NOT NULL DEFAULT 20,
 ADD COLUMN digest_ai_enabled BOOLEAN NOT NULL DEFAULT FALSE,
 ADD COLUMN digest_ai_language VARCHAR(2) NOT NULL DEFAULT 'zh';
UPDATE subscriptions SET max_items_per_digest=20 WHERE max_items_per_digest>20;
UPDATE subscriptions SET digest_ai_enabled=FALSE;
ALTER TABLE subscriptions
 ADD CONSTRAINT chk_subscription_digest_limit CHECK(max_items_per_digest BETWEEN 1 AND 20),
 ADD CONSTRAINT chk_subscription_ai_language CHECK(digest_ai_language IN ('zh','en'));

UPDATE users SET max_items_per_digest=20 WHERE max_items_per_digest>20;
UPDATE users SET ai_enabled=FALSE, ai_language=CASE WHEN ai_language='en' THEN 'en' ELSE 'zh' END;
ALTER TABLE users DROP CHECK chk_users_ai_language;
ALTER TABLE users
 MODIFY COLUMN max_items_per_digest SMALLINT UNSIGNED NOT NULL DEFAULT 20,
 ADD CONSTRAINT chk_users_ai_language CHECK(ai_language IN ('zh','en'));
ALTER TABLE users DROP COLUMN digest_ai_enabled;

-- The legacy unique index is also the supporting index for the scope foreign
-- key. Add a dedicated scope index before replacing that unique index.
ALTER TABLE paper_ai_summaries ADD INDEX idx_paper_ai_summaries_scope(scope_id);
ALTER TABLE paper_ai_summaries DROP INDEX uk_paper_ai_summaries_identity;
ALTER TABLE paper_ai_summaries
 ADD COLUMN owner_user_id BIGINT UNSIGNED NULL AFTER scope_id,
 ADD COLUMN subscription_id BIGINT UNSIGNED NULL AFTER owner_user_id,
 ADD COLUMN provider_id VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER language,
 ADD COLUMN model_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER provider_id,
 ADD COLUMN config_version BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER model_id,
 ADD UNIQUE KEY uk_paper_ai_summaries_identity(owner_user_id,scope_id,local_date,input_hash,language,profile,config_version),
 ADD INDEX idx_paper_ai_owner_config(owner_user_id,config_version,status),
 ADD CONSTRAINT fk_paper_ai_summaries_owner FOREIGN KEY(owner_user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE digest_ai_summaries ADD INDEX idx_digest_ai_summaries_scope(scope_id);
ALTER TABLE digest_ai_summaries DROP INDEX uk_digest_ai_summaries_identity;
ALTER TABLE digest_ai_summaries
 ADD COLUMN owner_user_id BIGINT UNSIGNED NULL AFTER scope_id,
 ADD COLUMN subscription_id BIGINT UNSIGNED NULL AFTER owner_user_id,
 ADD COLUMN provider_id VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER language,
 ADD COLUMN model_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER provider_id,
 ADD COLUMN config_version BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER model_id,
 ADD UNIQUE KEY uk_digest_ai_summaries_identity(owner_user_id,subscription_id,local_date,language,config_version),
 ADD INDEX idx_digest_ai_owner_config(owner_user_id,config_version,status),
 ADD CONSTRAINT fk_digest_ai_summaries_owner FOREIGN KEY(owner_user_id) REFERENCES users(id) ON DELETE CASCADE,
 ADD CONSTRAINT fk_digest_ai_summaries_subscription FOREIGN KEY(subscription_id) REFERENCES subscriptions(id) ON DELETE CASCADE;

UPDATE paper_ai_summaries SET status='failed',attempts=3,failure_code='legacy_platform_result',expires_at=COALESCE(expires_at,DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 30 DAY));
UPDATE digest_ai_summaries SET status='failed',attempts=3,failure_code='legacy_platform_result',expires_at=COALESCE(expires_at,DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 7 DAY));

-- +goose Down
ALTER TABLE digest_ai_summaries DROP FOREIGN KEY fk_digest_ai_summaries_subscription, DROP FOREIGN KEY fk_digest_ai_summaries_owner, DROP INDEX idx_digest_ai_owner_config, DROP INDEX uk_digest_ai_summaries_identity;
ALTER TABLE digest_ai_summaries DROP COLUMN config_version, DROP COLUMN model_id, DROP COLUMN provider_id, DROP COLUMN subscription_id, DROP COLUMN owner_user_id;
ALTER TABLE digest_ai_summaries ADD UNIQUE KEY uk_digest_ai_summaries_identity(scope_id,local_date,input_hash,language,profile);
ALTER TABLE digest_ai_summaries DROP INDEX idx_digest_ai_summaries_scope;
ALTER TABLE paper_ai_summaries DROP FOREIGN KEY fk_paper_ai_summaries_owner, DROP INDEX idx_paper_ai_owner_config, DROP INDEX uk_paper_ai_summaries_identity;
ALTER TABLE paper_ai_summaries DROP COLUMN config_version, DROP COLUMN model_id, DROP COLUMN provider_id, DROP COLUMN subscription_id, DROP COLUMN owner_user_id;
ALTER TABLE paper_ai_summaries ADD UNIQUE KEY uk_paper_ai_summaries_identity(scope_id,local_date,input_hash,language,profile);
ALTER TABLE paper_ai_summaries DROP INDEX idx_paper_ai_summaries_scope;
ALTER TABLE users DROP CHECK chk_users_ai_language;
ALTER TABLE users ADD COLUMN digest_ai_enabled BOOLEAN NOT NULL DEFAULT FALSE AFTER ai_language;
ALTER TABLE users MODIFY COLUMN max_items_per_digest SMALLINT UNSIGNED NOT NULL DEFAULT 50, ADD CONSTRAINT chk_users_ai_language CHECK(ai_language IN ('zh','en','both'));
ALTER TABLE subscriptions DROP CHECK chk_subscription_ai_language, DROP CHECK chk_subscription_digest_limit, DROP COLUMN digest_ai_language, DROP COLUMN digest_ai_enabled;
ALTER TABLE subscriptions MODIFY COLUMN max_items_per_digest SMALLINT UNSIGNED NOT NULL DEFAULT 50, ADD CONSTRAINT chk_subscription_digest_limit CHECK(max_items_per_digest BETWEEN 1 AND 50);
DROP TABLE ai_user_call_leases;
DROP TABLE ai_user_daily_usage;
DROP TABLE user_ai_configurations;
