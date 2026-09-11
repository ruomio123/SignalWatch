-- +goose Up
-- Keyset batches must seek by source and last committed id, without repeatedly
-- sorting the whole publication window as table statistics change.
CREATE INDEX idx_papers_source_cursor ON papers(source_id,id);

-- +goose Down
DROP INDEX idx_papers_source_cursor ON papers;
