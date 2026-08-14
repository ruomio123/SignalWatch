-- +goose Up
CREATE TABLE subscriptions(
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL,
    name VARCHAR(128) NOT NULL,-- 允许同一个用户使用重复的订阅名称
    enabled TINYINT(1) NOT NULL DEFAULT 1,-- tinyint表示布尔值,0=禁用，1=启用
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    deleted_at DATETIME(6) NULL DEFAULT NULL, -- 软删除标记
    INDEX idx_subscriptions_user_enabled (user_id, enabled),
    INDEX idx_subscriptions_user_deleted_at (user_id, deleted_at),
    CONSTRAINT fk_subscriptions_user 
        FOREIGN KEY(user_id) 
        REFERENCES users(id)
        ON UPDATE RESTRICT -- 不允许随意修改已经被订阅引用的用户主键
        ON DELETE RESTRICT -- 如果用户还有订阅记录，数据库拒绝物理删除该用户
)ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_unicode_ci;


-- +goose Down
DROP TABLE subscriptions;
