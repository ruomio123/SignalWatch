-- +goose Up
CREATE TABLE paper_document_structures (
 id CHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 document_id CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_version VARCHAR(128) NOT NULL,
 parser_version VARCHAR(64) NOT NULL,
 content_hash CHAR(64) NOT NULL,
 payload JSON NOT NULL,
 created_at DATETIME(6) NOT NULL,
 INDEX idx_document_structure(document_id),
 FOREIGN KEY(document_id) REFERENCES paper_documents(id) ON DELETE CASCADE
) ENGINE=InnoDB;

-- +goose Down
DROP TABLE paper_document_structures;
