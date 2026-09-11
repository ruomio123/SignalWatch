-- +goose Up
CREATE TABLE collection_leases (
 name VARCHAR(64) PRIMARY KEY,
 owner VARCHAR(64) NOT NULL,
 epoch BIGINT UNSIGNED NOT NULL DEFAULT 0,
 expires_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
-- +goose Down
DROP TABLE collection_leases;
