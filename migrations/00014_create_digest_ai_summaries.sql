-- +goose Up
CREATE TABLE digest_ai_summaries (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 scope_id BIGINT UNSIGNED NOT NULL,
 local_date VARCHAR(10) NOT NULL DEFAULT '',
 input_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 language VARCHAR(2) NOT NULL,
 profile CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 input_json JSON NOT NULL,
 status VARCHAR(16) NOT NULL DEFAULT 'pending',
 payload_json JSON NULL,
 priority INT NOT NULL DEFAULT 2,
 attempts INT NOT NULL DEFAULT 0,
 next_retry_at DATETIME(6) NOT NULL,
 lease_owner VARCHAR(64) NOT NULL DEFAULT '',
 lease_until DATETIME(6) NULL,
 failure_code VARCHAR(64) NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 expires_at DATETIME(6) NULL,
 UNIQUE KEY uk_digest_ai_summaries_identity(scope_id,local_date,input_hash,language,profile),
 INDEX idx_digest_ai_summaries_pending(status,next_retry_at,priority,id),
 CONSTRAINT fk_digest_ai_summaries_scope FOREIGN KEY(scope_id) REFERENCES users(id) ON DELETE CASCADE,
 CONSTRAINT chk_digest_ai_summaries_language CHECK(language IN ('zh','en')),
 CONSTRAINT chk_digest_ai_summaries_status CHECK(status IN ('pending','processing','ready','failed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
-- +goose Down
DROP TABLE digest_ai_summaries;
