#!/usr/bin/env bash
# This drill only addresses the named isolated Compose project, never MYSQL_DSN.
set -euo pipefail
cd "$(dirname "$0")/.."
compose=(docker compose -p signalwatch-refactor-test -f deploy/compose.test.yaml)
name="signalwatch_migration_${RANDOM}_test"
work=$(mktemp -d /tmp/signalwatch-migration.XXXXXX)
mysql(){ "${compose[@]}" exec -T -e MYSQL_PWD=isolated-test-only mysql mysql -uroot "$@"; }
mysql -e "CREATE DATABASE $name"
cleanup(){ mysql -e "DROP DATABASE $name"; rm -rf "$work"; }
trap cleanup EXIT
dsn="root:isolated-test-only@tcp(127.0.0.1:13306)/${name}?parseTime=true&loc=UTC"
goose -dir migrations mysql "$dsn" up-to 18
mysql "$name" <<'SQL'
INSERT INTO users(email,password_hash,timezone,digest_time,max_items_per_digest,status,role,created_at,updated_at) VALUES('migration@example.test','fixture','UTC','08:00:00',10,'active','user',UTC_TIMESTAMP(),UTC_TIMESTAMP());
INSERT INTO subscriptions(user_id,source_id,name,category,keywords_json,enabled,version,max_items_per_digest,digest_ai_enabled,digest_ai_language,created_at,updated_at) SELECT 1,id,'preserved','cs.AI','["agent"]',1,7,10,1,'en',UTC_TIMESTAMP(),UTC_TIMESTAMP() FROM sources WHERE source_key='arxiv';
INSERT INTO user_ai_configurations(user_id,provider_id,model_id,status,config_version,key_hint,secret_ciphertext,secret_nonce,master_key_version,created_at,updated_at) VALUES(1,'glm','glm-4.7-flash','active',3,'1234',X'00',X'00','fixture',UTC_TIMESTAMP(),UTC_TIMESTAMP());
SQL
"${compose[@]}" exec -T -e MYSQL_PWD=isolated-test-only mysql mysqldump -uroot --single-transaction --skip-comments --set-gtid-purged=OFF --no-tablespaces "$name" > "$work/before.sql"
# Simulate interruption after one DDL in 00020. Goose must not record success.
cp -R migrations "$work/migrations"
python3 - "$work/migrations/00020_ai_task_identity.sql" <<'PY'
from pathlib import Path
import sys
p=Path(sys.argv[1]);s=p.read_text();i=s.index(';')+1;p.write_text(s[:i]+"\nSIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected migration interruption';\n"+s[i:])
PY
if goose -dir "$work/migrations" mysql "$dsn" up; then
  echo 'fault injection did not fail' >&2; exit 1
fi
version=$(mysql -N "$name" -e 'SELECT MAX(version_id) FROM goose_db_version WHERE is_applied=1')
test "$version" = 19
# Restore the verified pre-cutover backup instead of guessing a partial Down.
mysql -e "DROP DATABASE $name; CREATE DATABASE $name"
mysql "$name" < "$work/before.sql"
test "$(mysql -N "$name" -e 'SELECT version FROM subscriptions WHERE name="preserved"')" = 7
goose -dir migrations mysql "$dsn" up-to 28
credential_before=$(mysql -N "$name" -e 'SELECT CONCAT(generation,":",config_version,":",HEX(secret_ciphertext),":",HEX(secret_nonce),":",master_key_version,":",is_default) FROM user_ai_configurations WHERE user_id=1')
goose -dir migrations mysql "$dsn" up-to 29
mysql "$name" <<'SQL'
INSERT INTO papers(source_id,arxiv_id,title,abstract,comments,authors_json,categories_json,arxiv_url,pdf_url,published_at,arxiv_updated_at,first_seen_at,created_at,updated_at)
 SELECT id,'1706.03762','Migration paper','A test abstract','','[]','["cs.AI"]','https://arxiv.org/abs/1706.03762','https://arxiv.org/pdf/1706.03762',UTC_TIMESTAMP(),UTC_TIMESTAMP(),UTC_TIMESTAMP(),UTC_TIMESTAMP(),UTC_TIMESTAMP() FROM sources WHERE source_key='arxiv';
INSERT INTO agent_conversations(id,user_id,kind,paper_id,title,active_run_id,created_at,updated_at)
 SELECT 'migration-paper',1,'paper',id,'Old paper conversation','migration-run',UTC_TIMESTAMP(),UTC_TIMESTAMP() FROM papers WHERE arxiv_id='1706.03762';
INSERT INTO agent_runs(id,conversation_id,user_id,idempotency_key,input_hash,question,provider,model,generation,version,context_mode,state,checkpoint,created_at,updated_at)
 SELECT 'migration-run','migration-paper',1,'migration-key',REPEAT('a',64),'Old question',provider_id,model_id,generation,config_version,'fulltext','pending','{"phase":"ready"}',UTC_TIMESTAMP(),UTC_TIMESTAMP() FROM user_ai_configurations WHERE user_id=1;
INSERT INTO agent_messages(conversation_id,run_id,role,content,citations,created_at)
 VALUES('migration-paper','migration-run','user','Preserved question','[]',UTC_TIMESTAMP());
SQL
goose -dir migrations mysql "$dsn" up
test "$(mysql -N "$name" -e 'SELECT CONCAT(generation,":",config_version,":",HEX(secret_ciphertext),":",HEX(secret_nonce),":",master_key_version,":",is_default) FROM user_ai_configurations WHERE user_id=1')" = "$credential_before"
test "$(mysql -N "$name" -e 'SELECT name FROM user_ai_configurations WHERE user_id=1')" = 'glm API'
test "$(mysql -N "$name" -e "SELECT generation<>'' AND config_version=3 FROM user_ai_configurations WHERE user_id=1")" = 1
test "$(mysql -N "$name" -e 'SELECT revision FROM ai_configuration_counters WHERE user_id=1')" = 3
test "$(mysql -N "$name" -e 'SELECT CONCAT(version,":",digest_ai_enabled,":",digest_ai_language) FROM subscriptions WHERE name="preserved"')" = '7:1:en'
test "$(mysql -N "$name" -e "SELECT CONCAT(state,':',failure_code) FROM agent_runs WHERE id='migration-run'")" = 'failed:workflow_changed'
test "$(mysql -N "$name" -e "SELECT active_run_id IS NULL FROM agent_conversations WHERE id='migration-paper'")" = 1
test "$(mysql -N "$name" -e "SELECT content FROM agent_messages WHERE run_id='migration-run'")" = 'Preserved question'
# Empty initialization has a separate database and exercises all historic migrations.
mysql -e "DROP DATABASE $name; CREATE DATABASE $name"
goose -dir migrations mysql "$dsn" up
test "$(mysql -N "$name" -e 'SELECT MAX(version_id) FROM goose_db_version WHERE is_applied=1')" = 30
echo 'migration drill: empty initialization, upgrade, interrupted DDL, backup restoration passed'
