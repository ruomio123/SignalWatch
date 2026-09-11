-- +goose Up
CREATE TABLE ai_call_records (
 id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 user_id BIGINT UNSIGNED NOT NULL,
 feature VARCHAR(16) NOT NULL,
 provider VARCHAR(32) NOT NULL,
 model VARCHAR(64) NOT NULL,
 generation VARCHAR(64) NOT NULL DEFAULT '',
 version BIGINT UNSIGNED NOT NULL DEFAULT 0,
 request_id VARCHAR(128) NOT NULL DEFAULT '',
 status VARCHAR(16) NOT NULL,
 failure_code VARCHAR(64) NOT NULL DEFAULT '',
 provider_code VARCHAR(128) NOT NULL DEFAULT '',
 provider_request_id VARCHAR(128) NOT NULL DEFAULT '',
 http_status INT NOT NULL DEFAULT 0,
 created_at DATETIME(6) NOT NULL,
 started_at DATETIME(6) NULL,
 finished_at DATETIME(6) NULL,
 lease_until DATETIME(6) NOT NULL,
 duration_ms BIGINT NOT NULL DEFAULT 0,
 input_tokens BIGINT NOT NULL DEFAULT 0,
 output_tokens BIGINT NOT NULL DEFAULT 0,
 usage_known BOOLEAN NOT NULL DEFAULT FALSE,
 INDEX idx_ai_calls_user(user_id,created_at,id),
 INDEX idx_ai_calls_recovery(status,lease_until),
 CONSTRAINT fk_ai_calls_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
 CONSTRAINT chk_ai_call_status CHECK(status IN ('reserved','started','succeeded','failed','unknown','cancelled'))
) ENGINE=InnoDB;
CREATE TABLE ai_call_admission (
 user_id BIGINT UNSIGNED PRIMARY KEY,
 lease_token VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
 lease_until DATETIME(6) NOT NULL,
 config_next_at DATETIME(6) NOT NULL,
 generation_next_at DATETIME(6) NOT NULL,
 CONSTRAINT fk_ai_admission_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB;
ALTER TABLE ai_user_daily_usage ADD COLUMN unknown INT UNSIGNED NOT NULL DEFAULT 0;
-- +goose Down
ALTER TABLE ai_user_daily_usage DROP COLUMN unknown;
DROP TABLE ai_call_admission;
DROP TABLE ai_call_records;
