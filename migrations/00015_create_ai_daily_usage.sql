-- +goose Up
CREATE TABLE ai_daily_usage (
 day DATE NOT NULL PRIMARY KEY,
 requests INT NOT NULL DEFAULT 0,
 input_tokens BIGINT NOT NULL DEFAULT 0,
 output_tokens BIGINT NOT NULL DEFAULT 0,
 estimated_calls INT NOT NULL DEFAULT 0,
 CONSTRAINT chk_ai_daily_requests CHECK (requests BETWEEN 0 AND 200),
 CONSTRAINT chk_ai_token_usage CHECK (input_tokens>=0 AND output_tokens>=0 AND estimated_calls>=0)
) ENGINE=InnoDB;
-- +goose Down
DROP TABLE ai_daily_usage;
