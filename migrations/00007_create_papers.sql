-- +goose Up
UPDATE sources
SET config_json = JSON_SET(
        config_json,
        '$.allowed_categories',
        JSON_ARRAY('cs.AI', 'cs.CL', 'cs.CV', 'cs.IR', 'cs.LG', 'cs.RO', 'cs.SE')
    ),
    updated_at = UTC_TIMESTAMP(6)
WHERE source_key = 'arxiv';

CREATE TABLE papers (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    source_id BIGINT UNSIGNED NOT NULL,
    arxiv_id VARCHAR(64) NOT NULL,
    title TEXT NOT NULL,
    abstract MEDIUMTEXT NOT NULL,
    authors_json JSON NOT NULL,
    categories_json JSON NOT NULL,
    published_at DATETIME(6) NOT NULL,
    arxiv_updated_at DATETIME(6) NOT NULL,
    arxiv_url VARCHAR(512) NOT NULL,
    pdf_url VARCHAR(512) NOT NULL,
    first_seen_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,

    UNIQUE KEY uk_papers_source_arxiv (source_id, arxiv_id),
    INDEX idx_papers_source_published (source_id, published_at),
    INDEX idx_papers_source_first_seen (source_id, first_seen_at),
    CONSTRAINT fk_papers_source
        FOREIGN KEY (source_id)
        REFERENCES sources (id)
        ON UPDATE RESTRICT
        ON DELETE RESTRICT
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE papers;

UPDATE sources
SET config_json = JSON_SET(
        config_json,
        '$.allowed_categories',
        JSON_ARRAY('cs.AI', 'cs.CL', 'cs.IR', 'cs.LG', 'cs.SE')
    ),
    updated_at = UTC_TIMESTAMP(6)
WHERE source_key = 'arxiv';
