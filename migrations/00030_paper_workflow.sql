-- +goose Up
ALTER TABLE agent_runs
 ADD COLUMN task VARCHAR(32) NOT NULL DEFAULT '',
 ADD COLUMN workflow_version VARCHAR(32) NOT NULL DEFAULT '',
 ADD COLUMN effective_context_mode VARCHAR(16) NOT NULL DEFAULT '',
 ADD COLUMN fallback_reason VARCHAR(64) NOT NULL DEFAULT '',
 ADD COLUMN batch_total INT NOT NULL DEFAULT 0,
 ADD COLUMN batch_completed INT NOT NULL DEFAULT 0;
ALTER TABLE agent_messages ADD COLUMN result JSON NULL;
ALTER TABLE paper_documents
 ADD COLUMN sections JSON NULL,
 ADD COLUMN text_complete BOOLEAN NOT NULL DEFAULT FALSE;

-- API and Worker must be stopped during migration. Old paper checkpoints
-- cannot be interpreted by the new fixed workflow. History remains readable.
UPDATE agent_runs r JOIN agent_conversations c ON c.id=r.conversation_id
 SET r.state='failed',r.progress='failed',r.failure_code='workflow_changed',r.updated_at=UTC_TIMESTAMP(6)
 WHERE c.kind='paper' AND r.state IN ('pending','running');
UPDATE agent_conversations c JOIN agent_runs r ON r.id=c.active_run_id
 SET c.active_run_id=NULL
 WHERE c.kind='paper' AND r.failure_code='workflow_changed';

-- +goose Down
ALTER TABLE paper_documents DROP COLUMN text_complete, DROP COLUMN sections;
ALTER TABLE agent_messages DROP COLUMN result;
ALTER TABLE agent_runs DROP COLUMN batch_completed, DROP COLUMN batch_total,
 DROP COLUMN fallback_reason, DROP COLUMN effective_context_mode,
 DROP COLUMN workflow_version, DROP COLUMN task;
