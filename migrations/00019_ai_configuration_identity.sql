-- +goose Up
CREATE TABLE ai_configuration_counters (
 user_id BIGINT UNSIGNED PRIMARY KEY,
 revision BIGINT UNSIGNED NOT NULL,
 CONSTRAINT fk_ai_counter_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB;
INSERT INTO ai_configuration_counters(user_id,revision) SELECT user_id,config_version FROM user_ai_configurations;
ALTER TABLE user_ai_configurations ADD COLUMN generation VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '';
UPDATE user_ai_configurations SET generation=REPLACE(UUID(),'-','') WHERE generation='';

-- +goose Down
-- Identity changes are forward-only. Restore a verified backup for rollback.
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='00019 is forward-only; restore a verified backup';
