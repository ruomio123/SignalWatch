-- +goose Up
-- Keep generation and encrypted bytes unchanged: existing runs and AAD remain valid.
ALTER TABLE user_ai_configurations
 DROP INDEX uk_ai_default,
 DROP PRIMARY KEY,
 MODIFY COLUMN default_slot VARCHAR(40) GENERATED ALWAYS AS (CASE WHEN is_default THEN ':default' ELSE NULL END) STORED,
 ADD PRIMARY KEY(user_id,generation),
 ADD UNIQUE KEY uk_ai_default(user_id,default_slot),
 ADD INDEX idx_ai_credentials_provider(user_id,provider_id),
 ADD COLUMN name VARCHAR(80) NOT NULL DEFAULT '';
UPDATE user_ai_configurations SET name=CONCAT(provider_id,' API') WHERE name='';

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='00029 is forward-only; restore a verified backup';
