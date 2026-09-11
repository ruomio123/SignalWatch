-- +goose Up
ALTER TABLE paper_ai_summaries ADD COLUMN config_generation VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '';
ALTER TABLE digest_ai_summaries ADD COLUMN config_generation VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '';
UPDATE paper_ai_summaries SET status='failed',attempts=1,failure_code='identity_migrated',lease_owner='',lease_until=NULL,expires_at=UTC_TIMESTAMP(6);
UPDATE digest_ai_summaries SET status='failed',attempts=1,failure_code='identity_migrated',lease_owner='',lease_until=NULL,expires_at=UTC_TIMESTAMP(6);
ALTER TABLE paper_ai_summaries DROP INDEX uk_paper_ai_summaries_identity, ADD UNIQUE KEY uk_paper_ai_summaries_identity(owner_user_id,scope_id,local_date,input_hash,language,profile,config_generation,config_version);
ALTER TABLE digest_ai_summaries DROP INDEX uk_digest_ai_summaries_identity, ADD UNIQUE KEY uk_digest_ai_summaries_identity(owner_user_id,subscription_id,local_date,language,config_generation,config_version);

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='00020 is forward-only; restore a verified backup';
