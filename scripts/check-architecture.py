#!/usr/bin/env python3
"""Fail when use cases regain persistence/framework construction dependencies."""
from pathlib import Path
import re
import sys
root = Path(__file__).resolve().parents[1]
application = [
    'internal/agent/*.go', 'internal/document/*.go', 'internal/rules/*.go', 'internal/insight/*.go', 'internal/generation/*.go',
    'internal/subscription/*.go', 'internal/backfill/*.go', 'internal/ai/*.go',
    'internal/auth/service.go', 'internal/auth/session.go', 'internal/digest/service.go', 'internal/digest/delivery.go', 'internal/user/service.go',
]
# Adapters may depend on frameworks; new use-case files are checked by default.
adapters = {'repository.go', 'handler.go', 'mysql.go'}

failures = []
for pattern in application:
    for path in root.glob(pattern):
        if path.name.endswith(('_test.go', '_mysql.go')) or path.name in adapters:
            continue
        text = path.read_text()
        for forbidden in ['gorm.io/', 'github.com/gin-gonic/', 'github.com/redis/',
                          '"database/sql"', 'internal/platform/db', 'internal/platform/llm', 'llm.NewProviderClient',
                          'NewRepository(', 'NewMySQL', '.Exec(', '.Raw(']:
            if forbidden in text:
                failures.append(f'{path.relative_to(root)}: forbidden use-case dependency {forbidden}')
for path in (root / 'internal').rglob('*.go'):
    if path.name.endswith('_test.go'):
        continue
    if re.search(r'func\s+\([^)]*\)\s+processLegacy\b', path.read_text()):
        failures.append(f'{path}: test-only execution path')
if failures:
    sys.exit('\n'.join(failures))
print('architecture boundaries: passed')
