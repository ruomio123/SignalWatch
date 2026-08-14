-- +goose Up
CREATE TABLE users (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    email VARCHAR(254) NOT NULL,
    password_hash VARCHAR(100) NOT NULL,
    timezone VARCHAR(64) NOT NULL DEFAULT 'UTC', -- 用户所属时区
    digest_time TIME NOT NULL DEFAULT '08:00:00', -- 用户当地希望收到摘要的时间
    max_papers_per_digest SMALLINT UNSIGNED NOT NULL DEFAULT 50, -- 每次摘要最多包含的论文数量
    status VARCHAR(16) NOT NULL DEFAULT 'active', -- 用户状态
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,

    UNIQUE KEY uk_users_email (email)-- INDEX=加速查询,UNIQUE KEY=加速查询+保证唯一性
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE users;