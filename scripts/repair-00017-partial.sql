-- Repair only the exact partial state left by the original 00017 migration
-- failing at:
--   ALTER TABLE paper_ai_summaries DROP INDEX uk_paper_ai_summaries_identity
--
-- Historical repair only: 00017 must not be marked applied. This script is not
-- part of normal startup or upgrades and must not run on an upgraded database.
-- From 00032 onward ai_user_call_leases is intentionally absent; running this
-- script there fails on the legacy row-count query. Keep it for old backups.
--
-- This restores the schema shape expected after migration 00016. It cannot
-- restore user preference values already reset by 00017; the corrected 00017
-- intentionally resets those values again when reapplied.

DELIMITER //

DROP PROCEDURE IF EXISTS signalwatch_repair_00017_partial//
CREATE PROCEDURE signalwatch_repair_00017_partial()
BEGIN
    DECLARE applied_00017 INT DEFAULT 0;
    DECLARE new_tables INT DEFAULT 0;
    DECLARE new_table_rows BIGINT DEFAULT 0;
    DECLARE subscription_columns INT DEFAULT 0;
    DECLARE user_digest_columns INT DEFAULT 0;
    DECLARE new_paper_columns INT DEFAULT 0;
    DECLARE legacy_indexes INT DEFAULT 0;
    DECLARE replacement_scope_indexes INT DEFAULT 0;
    DECLARE expected_checks INT DEFAULT 0;

    SELECT COUNT(*) INTO applied_00017
      FROM goose_db_version
     WHERE version_id = 17 AND is_applied = 1;

    SELECT COUNT(*) INTO new_tables
      FROM information_schema.tables
     WHERE table_schema = DATABASE()
       AND table_name IN ('user_ai_configurations', 'ai_user_daily_usage', 'ai_user_call_leases');

    SELECT
        (SELECT COUNT(*) FROM user_ai_configurations) +
        (SELECT COUNT(*) FROM ai_user_daily_usage) +
        (SELECT COUNT(*) FROM ai_user_call_leases)
      INTO new_table_rows;

    SELECT COUNT(*) INTO subscription_columns
      FROM information_schema.columns
     WHERE table_schema = DATABASE()
       AND table_name = 'subscriptions'
       AND column_name IN ('digest_ai_enabled', 'digest_ai_language');

    SELECT COUNT(*) INTO user_digest_columns
      FROM information_schema.columns
     WHERE table_schema = DATABASE()
       AND table_name = 'users'
       AND column_name = 'digest_ai_enabled';

    SELECT COUNT(*) INTO new_paper_columns
      FROM information_schema.columns
     WHERE table_schema = DATABASE()
       AND table_name IN ('paper_ai_summaries', 'digest_ai_summaries')
       AND column_name IN ('owner_user_id', 'subscription_id', 'provider_id', 'model_id', 'config_version');

    SELECT COUNT(DISTINCT CONCAT(table_name, ':', index_name)) INTO legacy_indexes
      FROM information_schema.statistics
     WHERE table_schema = DATABASE()
       AND (table_name, index_name) IN (
           ('paper_ai_summaries', 'uk_paper_ai_summaries_identity'),
           ('digest_ai_summaries', 'uk_digest_ai_summaries_identity')
       );

    SELECT COUNT(DISTINCT CONCAT(table_name, ':', index_name)) INTO replacement_scope_indexes
      FROM information_schema.statistics
     WHERE table_schema = DATABASE()
       AND (table_name, index_name) IN (
           ('paper_ai_summaries', 'idx_paper_ai_summaries_scope'),
           ('digest_ai_summaries', 'idx_digest_ai_summaries_scope')
       );

    SELECT COUNT(*) INTO expected_checks
      FROM information_schema.table_constraints
     WHERE table_schema = DATABASE()
       AND constraint_type = 'CHECK'
       AND (table_name, constraint_name) IN (
           ('subscriptions', 'chk_subscription_ai_language'),
           ('subscriptions', 'chk_subscription_digest_limit'),
           ('users', 'chk_users_ai_language')
       );

    IF applied_00017 <> 0
       OR new_tables <> 3
       OR new_table_rows <> 0
       OR subscription_columns <> 2
       OR user_digest_columns <> 0
       OR new_paper_columns <> 0
       OR legacy_indexes <> 2
       OR replacement_scope_indexes <> 0
       OR expected_checks <> 3 THEN
        SIGNAL SQLSTATE '45000'
            SET MESSAGE_TEXT = 'database is not in the expected partial 00017 state; no repair was applied';
    END IF;

    ALTER TABLE subscriptions
        DROP CHECK chk_subscription_ai_language,
        DROP CHECK chk_subscription_digest_limit,
        DROP COLUMN digest_ai_language,
        DROP COLUMN digest_ai_enabled;
    ALTER TABLE subscriptions
        MODIFY COLUMN max_items_per_digest SMALLINT UNSIGNED NOT NULL DEFAULT 50,
        ADD CONSTRAINT chk_subscription_digest_limit
            CHECK(max_items_per_digest BETWEEN 1 AND 50);

    ALTER TABLE users DROP CHECK chk_users_ai_language;
    ALTER TABLE users
        ADD COLUMN digest_ai_enabled BOOLEAN NOT NULL DEFAULT FALSE AFTER ai_language,
        MODIFY COLUMN max_items_per_digest SMALLINT UNSIGNED NOT NULL DEFAULT 50,
        ADD CONSTRAINT chk_users_ai_language CHECK(ai_language IN ('zh','en','both'));

    DROP TABLE ai_user_call_leases;
    DROP TABLE ai_user_daily_usage;
    DROP TABLE user_ai_configurations;
END//

CALL signalwatch_repair_00017_partial()//
DROP PROCEDURE signalwatch_repair_00017_partial//

DELIMITER ;
