#!/usr/bin/env python3
"""Explicit live evaluation through the authenticated public API; never confirms drafts.
Results record observed behavior. Semantic quality is scored by a separate human review.
"""
import argparse
import json
import os
import statistics
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-url', default='http://127.0.0.1:8080')
    parser.add_argument('--provider', required=True)
    parser.add_argument('--model', required=True)
    parser.add_argument('--credential-id', default='', help='API credential ID; required when a provider has multiple keys')
    parser.add_argument('--paper-map', type=Path, help='JSON map of arXiv IDs to owned local paper IDs')
    parser.add_argument('--cases', type=Path, default=Path('evals/agent/cases.jsonl'))
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--kind', choices=['paper', 'subscription'])
    parser.add_argument('--limit', type=int, default=80)
    parser.add_argument('--report', action='store_true', help='Generate one fixed report per paper instead of individual follow-ups')
    args = parser.parse_args()
    parsed = urllib.parse.urlparse(args.base_url)
    if parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in ('', '/'):
        parser.error('base URL must be an origin without credentials')
    if parsed.scheme != 'https' and not (parsed.scheme == 'http' and parsed.hostname in ('127.0.0.1', 'localhost', '::1')):
        parser.error('use HTTPS or a loopback origin')
    token = os.environ.get('SIGNALWATCH_EVAL_TOKEN')
    if not token:
        parser.error('set SIGNALWATCH_EVAL_TOKEN for an isolated evaluation user with configured credentials')
    if args.limit < 1 or args.limit > 80:
        parser.error('limit must be between 1 and 80')
    mapping = json.loads(args.paper_map.read_text()) if args.paper_map else {}
    cases = [json.loads(line) for line in args.cases.read_text().splitlines() if line.strip()]
    cases = [case for case in cases if not args.kind or case['kind'] == args.kind]

    if args.report:
        seen = set()
        reports = []
        for case in cases:
            if case['kind'] == 'paper' and case['arxiv_id'] not in seen:
                seen.add(case['arxiv_id'])
                reports.append(case)
        cases = reports
    cases = cases[:args.limit]

    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None
    opener = urllib.request.build_opener(NoRedirect)

    def call(path, body=None):
        request = urllib.request.Request(args.base_url.rstrip('/') + '/api/v2' + path,
            data=json.dumps(body).encode() if body is not None else None,
            headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
        try:
            with opener.open(request, timeout=35) as response:
                return json.load(response)
        except urllib.error.HTTPError as exc:
            # Never persist an upstream error body or token.
            raise RuntimeError('api_http_' + str(exc.code)) from None
        except (urllib.error.URLError, TimeoutError):
            raise RuntimeError('api_transport_failed') from None

    def wait_run(run, seconds):
        deadline = time.monotonic() + seconds
        while True:
            observed = call('/agent/runs/' + run['run_id'])
            if observed['run']['state'] not in ('pending', 'running'):
                return observed
            if time.monotonic() >= deadline:
                raise RuntimeError('evaluation_wait_timeout')
            time.sleep(2)

    results = []
    # Exclusive creation prevents accidentally replacing previous evaluation evidence.
    with args.output.open('x', encoding='utf-8') as output:
        for case in cases:
            result = {'case_id': case['id'], 'kind': case['kind'], 'task': 'paper_report' if args.report else case['kind'],
                      'expected': {'fields': ['problem', 'method', 'experiments', 'results', 'limitations']} if args.report else case['expected'],
                      'provider': args.provider, 'model': args.model, 'human_review': None}
            started = time.monotonic()
            try:
                body = {'kind': case['kind']}
                if case['kind'] == 'paper':
                    paper_id = mapping.get(case['arxiv_id'])
                    if not paper_id:
                        raise RuntimeError('not_run_missing_owned_paper_mapping')
                    body['paper_id'] = int(paper_id)
                conversation = call('/agent/conversations', body)
                result['conversation_id'] = conversation['id']
                preparation_steps = []
                if case['kind'] == 'paper' and not args.report:
                    preparation = call('/agent/conversations/' + conversation['id'] + '/messages', {
                        'task': 'paper_report', 'provider': args.provider, 'model': args.model,
                        'credential_id': args.credential_id, 'context_mode': 'fulltext',
                        'idempotency_key': 'eval-report-' + case['id']})
                    prepared = wait_run(preparation, 960)
                    preparation_steps = prepared['steps']
                    result['report_run_id'] = preparation['run_id']
                    result['report_steps'] = preparation_steps
                    result['model_calls'] = sum(step['kind'] == 'model' for step in preparation_steps)
                    result['input_tokens'] = sum(step['input_tokens'] for step in preparation_steps)
                    result['output_tokens'] = sum(step['output_tokens'] for step in preparation_steps)
                    if prepared['run']['state'] != 'completed':
                        raise RuntimeError('prerequisite_report_' + prepared['run']['state'])
                run = call('/agent/conversations/' + conversation['id'] + '/messages', {
                    'question': case['question'], 'provider': args.provider, 'model': args.model,
                    **({'task': 'paper_report' if args.report else 'paper_followup'} if case['kind'] == 'paper' else {}),
                    'credential_id': args.credential_id,
                    'context_mode': 'fulltext', 'idempotency_key': 'eval-' + case['id']})
                observed = wait_run(run, 960 if args.report else 240)
                messages = call('/agent/conversations/' + conversation['id'] + '/messages')['items']
                result.update(status=observed['run']['state'], failure_code=observed['run'].get('failure_code'),
                              run_id=run['run_id'], steps=observed['steps'], messages=messages)
                all_steps = preparation_steps + observed['steps']
                result['model_calls'] = sum(step['kind'] == 'model' for step in all_steps)
                result['input_tokens'] = sum(step['input_tokens'] for step in all_steps)
                result['output_tokens'] = sum(step['output_tokens'] for step in all_steps)
                for message in messages:
                    if message.get('draft_id'):
                        result['draft'] = call('/agent/subscription-drafts/' + message['draft_id'])
            except (RuntimeError, KeyError, ValueError) as exc:
                result['status'] = 'not_run' if str(exc).startswith('not_run_') else 'evaluation_failed'
                result['failure_code'] = str(exc) if isinstance(exc, RuntimeError) else 'invalid_api_response'
            result['duration_seconds'] = round(time.monotonic() - started, 3)
            output.write(json.dumps(result, ensure_ascii=False) + '\n')
            output.flush()
            results.append(result)
            print(case['id'], result['status'], flush=True)
    durations = sorted(r['duration_seconds'] for r in results if r['status'] != 'not_run')
    attempted = sum(r['status'] != 'not_run' for r in results)
    summary = {'cases': len(results), 'attempted': attempted,
        'completed': sum(r['status'] == 'completed' for r in results),
        'latency_p50_seconds': statistics.median(durations) if durations else None,
        'latency_p95_seconds': durations[min(len(durations)-1, int(len(durations)*.95))] if durations else None,
        'input_tokens': sum(r.get('input_tokens', 0) for r in results),
        'output_tokens': sum(r.get('output_tokens', 0) for r in results),
        'model_calls': sum(r.get('model_calls', 0) for r in results),
        'semantic_quality': 'pending human review; successful execution is not factual correctness'}
    args.output.with_suffix('.summary.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2))

if __name__ == '__main__':
    main()
