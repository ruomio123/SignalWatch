-- +goose Up
CREATE TABLE sources (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    source_key VARCHAR(64) NOT NULL,
    kind VARCHAR(16) NOT NULL,
    name VARCHAR(100) NOT NULL,
    endpoint VARCHAR(512) NULL,
    enabled TINYINT(1) NOT NULL DEFAULT 1,
    config_json JSON NOT NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,

    UNIQUE KEY uk_sources_source_key (source_key),
    CONSTRAINT chk_sources_kind CHECK (kind IN ('arxiv', 'rss'))
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_unicode_ci;

INSERT INTO sources (
    source_key,
    kind,
    name,
    endpoint,
    config_json,
    created_at,
    updated_at
) VALUES (
    'arxiv',
    'arxiv',
    'arXiv',
    'https://export.arxiv.org/api/query',
    JSON_OBJECT(
        'allowed_categories',
        JSON_ARRAY('cs.AI', 'cs.CL', 'cs.IR', 'cs.LG', 'cs.SE')
    ),
    UTC_TIMESTAMP(6),
    UTC_TIMESTAMP(6)
);

-- +goose Down
DROP TABLE sources;
