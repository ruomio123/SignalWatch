-- +goose Up
-- Back up these legacy tables before upgrading if their historical data is needed.
-- Current usage accounting and admission use ai_user_daily_usage and ai_call_admission.
-- IF EXISTS allows retry after interruption between the two nontransactional DDLs.
DROP TABLE IF EXISTS ai_daily_usage;
DROP TABLE IF EXISTS ai_user_call_leases;

-- +goose Down
-- Restore the original schema only. Deleted rows require the pre-upgrade backup.
CREATE TABLE ai_daily_usage (
 day DATE NOT NULL PRIMARY KEY,
 requests INT NOT NULL DEFAULT 0,
 input_tokens BIGINT NOT NULL DEFAULT 0,
 output_tokens BIGINT NOT NULL DEFAULT 0,
 estimated_calls INT NOT NULL DEFAULT 0,
 CONSTRAINT chk_ai_daily_requests CHECK (requests BETWEEN 0 AND 200),
 CONSTRAINT chk_ai_token_usage CHECK (input_tokens>=0 AND output_tokens>=0 AND estimated_calls>=0)
) ENGINE=InnoDB;

CREATE TABLE ai_user_call_leases (
 user_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
 lease_token VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 lease_until DATETIME(6) NOT NULL,
 CONSTRAINT fk_ai_user_call_leases_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB;
