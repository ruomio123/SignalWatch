-- +goose Up
CREATE TABLE agent_conversations (
 id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 user_id BIGINT UNSIGNED NOT NULL,
 kind VARCHAR(24) NOT NULL,
 paper_id BIGINT UNSIGNED NULL,
 title VARCHAR(160) NOT NULL,
 active_run_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
 latest_draft_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL, updated_at DATETIME(6) NOT NULL,
 INDEX idx_conversation_user(user_id,updated_at),
 FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
 FOREIGN KEY(paper_id) REFERENCES papers(id) ON DELETE CASCADE,
 CHECK(kind IN ('subscription','paper'))
) ENGINE=InnoDB;
CREATE TABLE agent_runs (
 id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 conversation_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 user_id BIGINT UNSIGNED NOT NULL,
 idempotency_key VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 input_hash CHAR(64) NOT NULL,
 question TEXT NOT NULL,
 provider VARCHAR(32) NOT NULL, model VARCHAR(64) NOT NULL,
 generation VARCHAR(64) NOT NULL, version BIGINT UNSIGNED NOT NULL,
 context_mode VARCHAR(16) NOT NULL,
 state VARCHAR(24) NOT NULL, progress VARCHAR(64) NOT NULL DEFAULT '',
 failure_code VARCHAR(64) NOT NULL DEFAULT '',
 checkpoint JSON NOT NULL,
 lease_owner VARCHAR(64) NOT NULL DEFAULT '', epoch BIGINT UNSIGNED NOT NULL DEFAULT 0,
 lease_until DATETIME(6) NULL, deadline DATETIME(6) NULL,
 created_at DATETIME(6) NOT NULL, updated_at DATETIME(6) NOT NULL,
 UNIQUE KEY uk_agent_submit(conversation_id,idempotency_key),
 INDEX idx_agent_dispatch(state,lease_until,created_at),
 FOREIGN KEY(conversation_id) REFERENCES agent_conversations(id) ON DELETE CASCADE,
 FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
 CHECK(state IN ('pending','running','completed','failed','unknown','cancelled')),
 CHECK(context_mode IN ('fulltext','abstract'))
) ENGINE=InnoDB;
CREATE TABLE agent_messages (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 conversation_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 run_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 role VARCHAR(16) NOT NULL, content TEXT NOT NULL,
 provider VARCHAR(32) NOT NULL DEFAULT '', model VARCHAR(64) NOT NULL DEFAULT '',
 citations JSON NOT NULL, draft_id VARCHAR(64) NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL,
 UNIQUE KEY uk_message_run_role(run_id,role),
 INDEX idx_messages(conversation_id,id),
 FOREIGN KEY(conversation_id) REFERENCES agent_conversations(id) ON DELETE CASCADE,
 FOREIGN KEY(run_id) REFERENCES agent_runs(id) ON DELETE CASCADE
) ENGINE=InnoDB;
CREATE TABLE agent_steps (
 run_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 sequence INT NOT NULL, kind VARCHAR(24) NOT NULL, tool VARCHAR(64) NOT NULL DEFAULT '',
 call_id VARCHAR(64) NOT NULL DEFAULT '', duration_ms BIGINT NOT NULL DEFAULT 0,
 input_tokens BIGINT NOT NULL DEFAULT 0, output_tokens BIGINT NOT NULL DEFAULT 0,
 failure_code VARCHAR(64) NOT NULL DEFAULT '', created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(run_id,sequence), FOREIGN KEY(run_id) REFERENCES agent_runs(id) ON DELETE CASCADE
) ENGINE=InnoDB;
CREATE TABLE agent_subscription_drafts (
 id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 conversation_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 user_id BIGINT UNSIGNED NOT NULL, version INT UNSIGNED NOT NULL,
 payload JSON NOT NULL, subscription_id BIGINT UNSIGNED NULL,
 expires_at DATETIME(6) NOT NULL, created_at DATETIME(6) NOT NULL,
 FOREIGN KEY(conversation_id) REFERENCES agent_conversations(id) ON DELETE CASCADE,
 FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
 FOREIGN KEY(subscription_id) REFERENCES subscriptions(id)
) ENGINE=InnoDB;
CREATE TABLE paper_documents (
 id CHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 paper_id BIGINT UNSIGNED NOT NULL, source_version VARCHAR(128) NOT NULL,
 parser_version VARCHAR(64) NOT NULL,
 content_hash CHAR(64) NOT NULL DEFAULT '', state VARCHAR(16) NOT NULL,
 failure_code VARCHAR(64) NOT NULL DEFAULT '', page_count INT NOT NULL DEFAULT 0,
 lease_owner VARCHAR(64) NOT NULL DEFAULT '', epoch BIGINT UNSIGNED NOT NULL DEFAULT 0,
 lease_until DATETIME(6) NULL,
 created_at DATETIME(6) NOT NULL, updated_at DATETIME(6) NOT NULL,
 INDEX idx_document_pending(state,created_at),
 FOREIGN KEY(paper_id) REFERENCES papers(id) ON DELETE CASCADE
) ENGINE=InnoDB;
CREATE TABLE paper_document_chunks (
 document_id CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 number INT NOT NULL, page INT NOT NULL, text MEDIUMTEXT NOT NULL,
 PRIMARY KEY(document_id,number),
 FOREIGN KEY(document_id) REFERENCES paper_documents(id) ON DELETE CASCADE
) ENGINE=InnoDB;
-- +goose Down
DROP TABLE paper_document_chunks;
DROP TABLE paper_documents;
DROP TABLE agent_subscription_drafts;
DROP TABLE agent_steps;
DROP TABLE agent_messages;
DROP TABLE agent_runs;
DROP TABLE agent_conversations;
