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
goose -dir migrations mysql "$dsn" up-to 30
# Preserve ordinary users and both active and disabled former operators.
mysql "$name" <<'SQL'
INSERT INTO users(email,password_hash,timezone,digest_time,max_items_per_digest,status,role,created_at,updated_at)
 VALUES ('former-operator@example.test','preserved-hash','Asia/Shanghai','09:10:00',12,'active','operator',UTC_TIMESTAMP(),UTC_TIMESTAMP()),
        ('disabled-operator@example.test','disabled-hash','UTC','10:00:00',9,'disabled','operator',UTC_TIMESTAMP(),UTC_TIMESTAMP());
INSERT INTO auth_sessions(token_hash,user_id,expires_at,created_at)
 SELECT REPEAT('c',64),id,UTC_TIMESTAMP()+INTERVAL 1 DAY,UTC_TIMESTAMP() FROM users WHERE email='former-operator@example.test';
SQL
# All user attributes except the intentionally removed role must survive unchanged.
users_query='SELECT JSON_ARRAY(id,email,password_hash,timezone,digest_time,max_items_per_digest,status,ai_enabled,ai_language,created_at,updated_at) FROM users ORDER BY id'
users_before=$(mysql -N "$name" -e "$users_query")
sessions_before=$(mysql -N "$name" -e 'SELECT * FROM auth_sessions ORDER BY token_hash')
"${compose[@]}" exec -T -e MYSQL_PWD=isolated-test-only mysql mysqldump -uroot --single-transaction --skip-comments --set-gtid-purged=OFF --no-tablespaces "$name" > "$work/before-role-removal.sql"
goose -dir migrations mysql "$dsn" up-to 31
assert_role_removal(){
  test "$(mysql -N "$name" -e "$users_query")" = "$users_before"
  test "$(mysql -N "$name" -e 'SELECT * FROM auth_sessions ORDER BY token_hash')" = "$sessions_before"
  test "$(mysql -N "$name" -e "SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='$name' AND TABLE_NAME='users' AND COLUMN_NAME='role'")" = 0
  test "$(mysql -N "$name" -e "SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA='$name' AND TABLE_NAME='users' AND CONSTRAINT_NAME='chk_users_role'")" = 0
  test "$(mysql -N "$name" -e "SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='$name' AND TABLE_NAME='users' AND INDEX_NAME='idx_users_role_status'")" = 0
}
assert_role_removal
# Down restores only the schema, with user as the default for every account.
goose -dir migrations mysql "$dsn" down-to 30
test "$(mysql -N "$name" -e 'SELECT COUNT(*) FROM users WHERE role<>"user"')" = 0
test "$(mysql -N "$name" -e "$users_query")" = "$users_before"
if mysql "$name" -e "UPDATE users SET role='invalid' WHERE id=1" 2>/dev/null; then
  echo 'role CHECK was not restored by Down' >&2; exit 1
fi
test "$(mysql -N "$name" -e "SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='$name' AND TABLE_NAME='users' AND INDEX_NAME='idx_users_role_status'")" = 2
goose -dir migrations mysql "$dsn" up-to 31
assert_role_removal
# Rehearse restoring historical assignments from the backup, then upgrading again.
mysql -e "DROP DATABASE $name; CREATE DATABASE $name"
mysql "$name" < "$work/before-role-removal.sql"
test "$(mysql -N "$name" -e 'SELECT COUNT(*) FROM users WHERE role="operator"')" = 2
goose -dir migrations mysql "$dsn" up-to 31
assert_role_removal
test "$(mysql -N "$name" -e 'SELECT CONCAT(generation,":",config_version,":",HEX(secret_ciphertext),":",HEX(secret_nonce),":",master_key_version,":",is_default) FROM user_ai_configurations WHERE user_id=1')" = "$credential_before"
test "$(mysql -N "$name" -e 'SELECT name FROM user_ai_configurations WHERE user_id=1')" = 'glm API'
test "$(mysql -N "$name" -e "SELECT generation<>'' AND config_version=3 FROM user_ai_configurations WHERE user_id=1")" = 1
test "$(mysql -N "$name" -e 'SELECT revision FROM ai_configuration_counters WHERE user_id=1')" = 3
test "$(mysql -N "$name" -e 'SELECT CONCAT(version,":",digest_ai_enabled,":",digest_ai_language) FROM subscriptions WHERE name="preserved"')" = '7:1:en'
test "$(mysql -N "$name" -e "SELECT CONCAT(state,':',failure_code) FROM agent_runs WHERE id='migration-run'")" = 'failed:workflow_changed'
test "$(mysql -N "$name" -e "SELECT active_run_id IS NULL FROM agent_conversations WHERE id='migration-paper'")" = 1
test "$(mysql -N "$name" -e "SELECT content FROM agent_messages WHERE run_id='migration-run'")" = 'Preserved question'

# Exercise 00032 with data in both obsolete tables and every retained table.
mysql "$name" <<'SQL'
SET @paper = (SELECT id FROM papers WHERE arxiv_id='1706.03762');
SET @subscription = (SELECT id FROM subscriptions WHERE name='preserved');
SET @source = (SELECT id FROM sources WHERE source_key='arxiv');
INSERT INTO ai_daily_usage(day,requests,input_tokens,output_tokens,estimated_calls)
 VALUES('2026-09-01',7,123,45,1);
INSERT INTO ai_user_call_leases(user_id,lease_token,lease_until)
 VALUES(1,'legacy-lease','2026-09-01 00:01:00');
INSERT INTO subscription_papers(subscription_id,paper_id,matched_keywords_json,matched_at)
 VALUES(@subscription,@paper,'["agent"]','2026-09-01');
INSERT INTO subscription_backfills(subscription_id,source_id,category,keywords_json,window_from,window_to,cursor_id,state,processed,matched,next_retry_at,updated_at)
 VALUES(@subscription,@source,'cs.AI','["agent"]','2026-08-25','2026-09-01',@paper,'complete',1,1,'2026-09-01','2026-09-01');
INSERT INTO digest_deliveries(user_id,subscription_id,local_date,state,snapshot_json,next_retry_at,created_at,updated_at)
 VALUES(1,@subscription,'2026-09-01','retry','{"subject":"Preserved mail","text":"Original body"}','2026-09-01','2026-09-01','2026-09-01');
INSERT INTO digest_delivery_items(subscription_id,paper_id,delivery_id)
 VALUES(@subscription,@paper,LAST_INSERT_ID());
INSERT INTO collection_leases(name,owner,epoch,expires_at)
 VALUES('migration-collector','preserved-owner',7,'2026-09-01');
INSERT INTO ai_user_daily_usage(user_id,day,feature,calls,succeeded,failed,unknown,input_tokens,output_tokens,usage_missing)
 VALUES(1,'2026-09-01','paper_qa',3,1,1,1,123,45,1);
INSERT INTO ai_call_records(id,user_id,feature,provider,model,generation,version,status,created_at,started_at,finished_at,lease_until,duration_ms,input_tokens,output_tokens,usage_known)
 SELECT 'preserved-call',user_id,'paper_qa',provider_id,model_id,generation,config_version,'succeeded','2026-09-01','2026-09-01','2026-09-01 00:00:01','2026-09-01 00:01:00',1000,123,45,TRUE FROM user_ai_configurations WHERE user_id=1;
INSERT INTO ai_call_admission(user_id,lease_token,lease_until,config_next_at,generation_next_at)
 VALUES(1,'preserved-call','2026-09-01 00:01:00','2026-09-01 00:02:00','2026-09-01 00:03:00');
INSERT INTO paper_ai_summaries(scope_id,owner_user_id,input_hash,language,provider_id,model_id,config_version,config_generation,profile,input_json,status,payload_json,next_retry_at,created_at,updated_at)
 SELECT @paper,user_id,REPEAT('a',64),'zh',provider_id,model_id,config_version,generation,REPEAT('b',64),'{}','ready','{"summary":"Preserved paper summary"}','2026-09-01','2026-09-01','2026-09-01' FROM user_ai_configurations WHERE user_id=1;
INSERT INTO digest_ai_summaries(scope_id,owner_user_id,subscription_id,local_date,input_hash,language,provider_id,model_id,config_version,config_generation,profile,input_json,status,payload_json,next_retry_at,created_at,updated_at)
 SELECT user_id,user_id,@subscription,'2026-09-01',REPEAT('a',64),'zh',provider_id,model_id,config_version,generation,REPEAT('b',64),'{}','ready','{"overview":"Preserved digest"}','2026-09-01','2026-09-01','2026-09-01' FROM user_ai_configurations WHERE user_id=1;
INSERT INTO agent_steps(run_id,sequence,kind,tool,call_id,duration_ms,input_tokens,output_tokens,created_at)
 VALUES('migration-run',1,'model','report','preserved-call',1000,123,45,'2026-09-01');
INSERT INTO agent_conversations(id,user_id,kind,title,latest_draft_id,created_at,updated_at)
 VALUES('migration-subscription',1,'subscription','Preserved draft conversation','preserved-draft','2026-09-01','2026-09-01');
INSERT INTO agent_subscription_drafts(id,conversation_id,user_id,version,payload,subscription_id,expires_at,created_at)
 VALUES('preserved-draft','migration-subscription',1,2,'{"name":"Preserved draft"}',@subscription,'2026-09-02','2026-09-01');
INSERT INTO paper_documents(id,paper_id,source_version,parser_version,content_hash,state,page_count,sections,text_complete,created_at,updated_at)
 VALUES(REPEAT('d',64),@paper,'v1','fixture-parser',REPEAT('e',64),'ready',1,'[{"title":"Introduction","page":1,"line":1}]',TRUE,'2026-09-01','2026-09-01');
INSERT INTO paper_document_chunks(document_id,number,page,text)
 VALUES(REPEAT('d',64),1,1,'Preserved full text and citation evidence.');
SQL
dump(){
  "${compose[@]}" exec -T -e MYSQL_PWD=isolated-test-only mysql mysqldump -uroot \
    --single-transaction --skip-comments --order-by-primary --hex-blob \
    --set-gtid-purged=OFF --no-tablespaces "$name" "$@"
}
mapfile -t retained_tables < <(mysql -N "$name" -e "SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA='$name' AND TABLE_TYPE='BASE TABLE' AND TABLE_NAME NOT IN ('goose_db_version','ai_daily_usage','ai_user_call_leases') ORDER BY TABLE_NAME")
test "${#retained_tables[@]}" = 24
for table in "${retained_tables[@]}"; do
  test "$(mysql -N "$name" -e "SELECT EXISTS(SELECT 1 FROM $table)")" = 1
done
dump "${retained_tables[@]}" > "$work/retained-before.sql"
legacy_schema=$(mysql -N "$name" -e 'SHOW CREATE TABLE ai_daily_usage; SHOW CREATE TABLE ai_user_call_leases')
dump ai_daily_usage ai_user_call_leases > "$work/legacy-backup.sql"
assert_retained_unchanged(){
  dump "${retained_tables[@]}" > "$work/retained-after.sql"
  cmp "$work/retained-before.sql" "$work/retained-after.sql"
}
assert_legacy_removed(){
  test "$(mysql -N "$name" -e "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='$name' AND TABLE_NAME IN ('ai_daily_usage','ai_user_call_leases')")" = 0
  test "$(mysql -N "$name" -e "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='$name' AND TABLE_TYPE='BASE TABLE' AND TABLE_NAME<>'goose_db_version'")" = 24
}
goose -dir migrations mysql "$dsn" up-to 32
assert_legacy_removed
assert_retained_unchanged

# Down must restore the exact original definitions, but never fabricate old usage.
goose -dir migrations mysql "$dsn" down-to 31
test "$(mysql -N "$name" -e 'SHOW CREATE TABLE ai_daily_usage; SHOW CREATE TABLE ai_user_call_leases')" = "$legacy_schema"
test "$(mysql -N "$name" -e 'SELECT (SELECT COUNT(*) FROM ai_daily_usage)+(SELECT COUNT(*) FROM ai_user_call_leases)')" = 0
assert_retained_unchanged
mysql "$name" < "$work/legacy-backup.sql"
test "$(mysql -N "$name" -e "SELECT requests FROM ai_daily_usage WHERE day='2026-09-01'")" = 7
test "$(mysql -N "$name" -e 'SELECT lease_token FROM ai_user_call_leases WHERE user_id=1')" = legacy-lease

# Interrupt after the first DROP. Retry the real migration without restoring
# deleted legacy data or manually changing Goose's version ledger.
cp -R migrations "$work/cleanup-migrations"
python3 - "$work/cleanup-migrations/00032_remove_unused_ai_tables.sql" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
s = p.read_text()
first_drop = 'DROP TABLE IF EXISTS ai_daily_usage;'
assert s.count(first_drop) == 1
p.write_text(s.replace(first_drop, first_drop + "\nSIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected cleanup interruption';", 1))
PY
if goose -dir "$work/cleanup-migrations" mysql "$dsn" up-to 32; then
  echo 'cleanup fault injection did not fail' >&2; exit 1
fi
test "$(mysql -N "$name" -e 'SELECT MAX(version_id) FROM goose_db_version WHERE is_applied=1')" = 31
test "$(mysql -N "$name" -e "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='$name' AND TABLE_NAME='ai_daily_usage'")" = 0
test "$(mysql -N "$name" -e 'SELECT lease_token FROM ai_user_call_leases WHERE user_id=1')" = legacy-lease
assert_retained_unchanged
goose -dir migrations mysql "$dsn" up-to 32
assert_legacy_removed
assert_retained_unchanged

# Empty initialization has a separate database and exercises all historic migrations.
mysql -e "DROP DATABASE $name; CREATE DATABASE $name"
goose -dir migrations mysql "$dsn" up
test "$(mysql -N "$name" -e 'SELECT MAX(version_id) FROM goose_db_version WHERE is_applied=1')" = 32
assert_legacy_removed
echo 'migration drill: empty initialization, upgrade, interrupted DDL, backup restoration, role removal, legacy AI cleanup, 24-table preservation and rollback passed'
