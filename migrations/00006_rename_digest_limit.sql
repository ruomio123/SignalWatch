-- +goose Up
ALTER TABLE users
    RENAME COLUMN max_papers_per_digest TO max_items_per_digest;

-- +goose Down
ALTER TABLE users
    RENAME COLUMN max_items_per_digest TO max_papers_per_digest;
