-- +goose Up
CREATE TABLE subscription_rules (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    subscription_id BIGINT UNSIGNED NOT NULL, -- 规则必须属于某个订阅，因此不能为空
    rule_type VARCHAR(32) NOT NULL,
    rule_value VARCHAR(512) NOT NULL, -- 用于 API 回显，让用户看到适合展示的文本
    normalized_value VARCHAR(512) NOT NULL, -- 用于去重和后续匹配
    created_at DATETIME(6) NOT NULL,

    CONSTRAINT chk_subscription_rules_type
        CHECK (
            rule_type IN (
                'category', -- arxiv分类
                'author',  
                'include_keyword', -- 必须包含的关键词
                'exclude_keyword' -- 必须排除的关键词
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
        ON DELETE CASCADE -- 物理删除一条订阅时，数据库自动把属于该订阅的所有规则一起物理删除
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE subscription_rules;