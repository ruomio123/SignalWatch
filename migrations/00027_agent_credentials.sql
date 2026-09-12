-- +goose Up
ALTER TABLE user_ai_configurations
 ADD COLUMN is_default BOOLEAN NOT NULL DEFAULT TRUE,
 ADD COLUMN default_slot VARCHAR(40) GENERATED ALWAYS AS (CASE WHEN is_default THEN ":default" ELSE provider_id END) STORED,
 ADD UNIQUE KEY uk_ai_default(user_id,default_slot),
 DROP PRIMARY KEY, ADD PRIMARY KEY(user_id,provider_id);
ALTER TABLE ai_user_daily_usage DROP CHECK chk_ai_user_daily_usage_feature;
ALTER TABLE ai_user_daily_usage MODIFY feature VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 ADD CONSTRAINT chk_ai_user_daily_usage_feature CHECK(feature IN ('digest','paper','config_test','subscription_agent','paper_qa'));
ALTER TABLE ai_call_records MODIFY feature VARCHAR(32) NOT NULL;
-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='00027 is forward-only; restore a verified backup';
