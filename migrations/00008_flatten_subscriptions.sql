-- +goose Up
ALTER TABLE subscriptions
    ADD COLUMN category VARCHAR(32) NULL AFTER objective,
    ADD COLUMN keywords_json JSON NULL AFTER category;

UPDATE subscriptions
SET category = (
        SELECT rules.rule_value
        FROM subscription_rules AS rules
        WHERE rules.subscription_id = subscriptions.id
          AND rules.rule_type = 'category'
        ORDER BY rules.id
        LIMIT 1
    ),
    keywords_json = COALESCE(
        (
            SELECT JSON_ARRAYAGG(rules.rule_value)
            FROM subscription_rules AS rules
            WHERE rules.subscription_id = subscriptions.id
              AND rules.rule_type = 'include_keyword'
        ),
        JSON_ARRAY()
    );

ALTER TABLE subscriptions
    MODIFY COLUMN category VARCHAR(32) NOT NULL,
    MODIFY COLUMN keywords_json JSON NOT NULL,
    ADD CONSTRAINT chk_subscriptions_keywords_array
        CHECK (JSON_TYPE(keywords_json) = 'ARRAY'),
    ADD INDEX idx_subscriptions_source_category_active (
        source_id,
        category,
        enabled,
        deleted_at
    );

UPDATE sources
SET config_json = JSON_SET(
        config_json,
        '$.rule_types',
        JSON_ARRAY('category', 'include_keyword')
    ),
    updated_at = UTC_TIMESTAMP(6)
WHERE source_key = 'arxiv';

DROP TABLE subscription_rules;

-- +goose Down
CREATE TABLE subscription_rules (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    subscription_id BIGINT UNSIGNED NOT NULL,
    rule_type VARCHAR(32) NOT NULL,
    rule_value VARCHAR(512) NOT NULL,
    normalized_value VARCHAR(512) NOT NULL,
    created_at DATETIME(6) NOT NULL,

    CONSTRAINT chk_subscription_rules_type
        CHECK (
            rule_type IN (
                'category',
                'author',
                'include_keyword',
                'exclude_keyword'
            )
        ),
    UNIQUE KEY uk_subscription_rules_dedup (
        subscription_id,
        rule_type,
        normalized_value
    ),
    INDEX idx_subscription_rules_subscription_type (
        subscription_id,
        rule_type
    ),
    CONSTRAINT fk_subscription_rules_subscription
        FOREIGN KEY (subscription_id)
        REFERENCES subscriptions (id)
        ON UPDATE RESTRICT
        ON DELETE CASCADE
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_unicode_ci;

INSERT INTO subscription_rules (
    subscription_id,
    rule_type,
    rule_value,
    normalized_value,
    created_at
)
SELECT
    id,
    'category',
    category,
    category,
    UTC_TIMESTAMP(6)
FROM subscriptions;

INSERT INTO subscription_rules (
    subscription_id,
    rule_type,
    rule_value,
    normalized_value,
    created_at
)
SELECT
    subscriptions.id,
    'include_keyword',
    keywords.keyword,
    LOWER(keywords.keyword),
    UTC_TIMESTAMP(6)
FROM subscriptions
JOIN JSON_TABLE(
    subscriptions.keywords_json,
    '$[*]' COLUMNS(keyword VARCHAR(512) PATH '$')
) AS keywords;

ALTER TABLE subscriptions
    DROP INDEX idx_subscriptions_source_category_active,
    DROP CHECK chk_subscriptions_keywords_array,
    DROP COLUMN keywords_json,
    DROP COLUMN category;

UPDATE sources
SET config_json = JSON_SET(
        config_json,
        '$.rule_types',
        JSON_ARRAY('category', 'author', 'include_keyword', 'exclude_keyword')
    ),
    updated_at = UTC_TIMESTAMP(6)
WHERE source_key = 'arxiv';
