-- +goose Up
CREATE TABLE subscription_papers (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    subscription_id BIGINT UNSIGNED NOT NULL,
    paper_id BIGINT UNSIGNED NOT NULL,
    matched_keywords_json JSON NOT NULL,
    matched_at DATETIME(6) NOT NULL,
    delivered_at DATETIME(6) NULL DEFAULT NULL,

    CONSTRAINT chk_subscription_papers_keywords_array
        CHECK (JSON_TYPE(matched_keywords_json) = 'ARRAY'),
    UNIQUE KEY uk_subscription_papers_subscription_paper (
        subscription_id,
        paper_id
    ),
    INDEX idx_subscription_papers_delivery (
        subscription_id,
        delivered_at,
        paper_id
    ),
    INDEX idx_subscription_papers_paper_delivery (
        paper_id,
        delivered_at
    ),
    CONSTRAINT fk_subscription_papers_subscription
        FOREIGN KEY (subscription_id)
        REFERENCES subscriptions (id)
        ON UPDATE RESTRICT
        ON DELETE CASCADE,
    CONSTRAINT fk_subscription_papers_paper
        FOREIGN KEY (paper_id)
        REFERENCES papers (id)
        ON UPDATE RESTRICT
        ON DELETE CASCADE
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE subscription_papers;
