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
goose -dir migrations mysql "$dsn" up
test "$(mysql -N "$name" -e "SELECT generation<>'' AND config_version=3 FROM user_ai_configurations WHERE user_id=1")" = 1
test "$(mysql -N "$name" -e 'SELECT revision FROM ai_configuration_counters WHERE user_id=1')" = 3
test "$(mysql -N "$name" -e 'SELECT CONCAT(version,":",digest_ai_enabled,":",digest_ai_language) FROM subscriptions WHERE name="preserved"')" = '7:1:en'
# Empty initialization has a separate database and exercises all historic migrations.
mysql -e "DROP DATABASE $name; CREATE DATABASE $name"
goose -dir migrations mysql "$dsn" up
test "$(mysql -N "$name" -e 'SELECT MAX(version_id) FROM goose_db_version WHERE is_applied=1')" = 26
echo 'migration drill: empty initialization, upgrade, interrupted DDL, backup restoration passed'
