#!/usr/bin/env bash
# Destructive only inside a newly created database on the isolated test project.
set -euo pipefail
cd "$(dirname "$0")/.."
compose=(docker compose -p signalwatch-refactor-test -f deploy/compose.test.yaml)
name="signalwatch_ai_migration_${RANDOM}_test"
work=$(mktemp -d /tmp/signalwatch-ai-migration.XXXXXX)
mysql(){ "${compose[@]}" exec -T -e MYSQL_PWD=isolated-test-only mysql mysql -uroot "$@"; }
cleanup(){ mysql -e "DROP DATABASE IF EXISTS $name"; rm -rf "$work"; }
trap cleanup EXIT
mysql -e "CREATE DATABASE $name"
dsn="root:isolated-test-only@tcp(127.0.0.1:13306)/${name}?parseTime=true&loc=UTC"
goose -dir migrations mysql "$dsn" up-to 25
mysql "$name" <<'SQL'
INSERT INTO users(email,password_hash,timezone,digest_time,max_items_per_digest,status,role,created_at,updated_at) VALUES('ai-migration@example.test','fixture','UTC','08:00:00',10,'active','user',UTC_TIMESTAMP(),UTC_TIMESTAMP());
INSERT INTO user_ai_configurations(user_id,generation,provider_id,model_id,status,config_version,key_hint,secret_ciphertext,secret_nonce,master_key_version,created_at,updated_at) VALUES(1,'preserved-generation','glm','glm-4.7-flash','active',7,'1234',X'00',X'00','fixture',UTC_TIMESTAMP(),UTC_TIMESTAMP());
INSERT INTO ai_configuration_counters(user_id,revision) VALUES(1,7);
INSERT INTO ai_user_daily_usage(user_id,day,feature,calls,failed) VALUES(1,UTC_DATE(),'config_test',7,7);
SQL
"${compose[@]}" exec -T -e MYSQL_PWD=isolated-test-only mysql mysqldump -uroot --single-transaction --skip-comments --set-gtid-purged=OFF --no-tablespaces "$name" > "$work/before.sql"
cp -R migrations "$work/migrations"
python3 - "$work/migrations/00026_ai_call_records.sql" <<'PY'
from pathlib import Path
import sys
p=Path(sys.argv[1]);s=p.read_text();i=s.index(';')+1
p.write_text(s[:i]+"\nSIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected AI migration interruption';\n"+s[i:])
PY
if goose -dir "$work/migrations" mysql "$dsn" up; then
  echo 'fault injection did not fail' >&2; exit 1
fi
test "$(mysql -N "$name" -e 'SELECT MAX(version_id) FROM goose_db_version WHERE is_applied=1')" = 25
mysql -e "DROP DATABASE $name; CREATE DATABASE $name"
mysql "$name" < "$work/before.sql"
goose -dir migrations mysql "$dsn" up
test "$(mysql -N "$name" -e 'SELECT CONCAT(generation,":",config_version) FROM user_ai_configurations WHERE user_id=1')" = 'preserved-generation:7'
test "$(mysql -N "$name" -e 'SELECT CONCAT(calls,":",failed,":",unknown) FROM ai_user_daily_usage WHERE user_id=1')" = '7:7:0'
test "$(mysql -N "$name" -e 'SELECT COUNT(*) FROM ai_call_records')" = 0
echo 'AI migration drill: v25 upgrade, partial DDL failure, backup restore, credentials and usage preservation passed'
