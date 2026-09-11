#!/usr/bin/env python3
"""Interactive local AI acceptance. Secrets travel only through an anonymous stdin pipe."""
import argparse
import getpass
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


def main():
    parser = argparse.ArgumentParser(description="本地 AI 验收：隐藏输入密钥，生成报告与邮件预览。")
    parser.add_argument("--papers", type=Path, help="论文 JSON 数组文件；省略时只读本地 MySQL 容器")
    parser.add_argument("--count", type=int, default=3, help="论文数，默认 3；正式验收用 30")
    parser.add_argument("--groups", type=int, default=1, help="三篇一组的多论文样本数，默认 1；正式验收用 10")
    parser.add_argument("--language", choices=["zh", "en", "both"], default="zh")
    parser.add_argument("--base-url", help="兼容接口前缀，程序追加 /chat/completions")
    parser.add_argument("--model", help="模型名称，未提供则交互输入")
    parser.add_argument("--output", type=Path, help="新建报告目录；默认在项目 tmp/ 下，拒绝覆盖已有目录")
    parser.add_argument("--mysql-container", default="deploy-mysql-1")
    parser.add_argument("--mailpit", default="", help="可选：本机 Mailpit SMTP 地址，例如 127.0.0.1:1025")
    args = parser.parse_args()
    languages = 2 if args.language == "both" else 1
    calls = (args.count + args.groups) * languages
    if args.count < 2 or args.count > 100 or args.groups < 1 or args.groups > (args.count + 2) // 3 or calls > 200:
        parser.error("需要 2–100 篇论文、1–ceil(count/3) 组；最多 200 次调用。")
    if not sys.stdin.isatty():
        parser.error("请在交互终端运行，以保证密钥隐藏输入。")
    root = Path(__file__).resolve().parents[1]
    # Build and load public paper data BEFORE requesting the credential.
    with tempfile.TemporaryDirectory(prefix="signalwatch-ai-runner-") as temp:
        binary = Path(temp) / "ai-accept"
        built = subprocess.run(["go", "build", "-o", str(binary), "./cmd/ai-accept"], cwd=root, capture_output=True)
        if built.returncode:
            print("构建验收程序失败。请先在项目中执行 go build ./cmd/ai-accept 排查。", file=sys.stderr)
            return 1
        if args.papers:
            papers = json.loads(args.papers.read_text())
        else:
            query = ("SELECT JSON_OBJECT('id',id,'title',title,'abstract',abstract) FROM papers "
                     "WHERE title<>'' AND abstract<>'' ORDER BY first_seen_at DESC,id DESC LIMIT " + str(args.count) + ";")
            read = subprocess.run(["docker", "exec", "-i", args.mysql_container, "sh", "-c",
                                   'MYSQL_PWD="$MYSQL_PASSWORD" exec mysql --default-character-set=utf8mb4 -u"$MYSQL_USER" "$MYSQL_DATABASE" -N -B -r'],
                                  input=query, text=True, capture_output=True)
            if read.returncode:
                print("读取本地论文失败；请检查 Docker/MySQL，或使用 --papers 指定 JSON 文件。", file=sys.stderr)
                return 1
            papers = [json.loads(line) for line in read.stdout.splitlines() if line.strip()]
        if not isinstance(papers, list) or len(papers) < args.count:
            parser.error("论文数量不足；请减少 --count 或提供更多论文。")
        papers = papers[:args.count]
        ids = set()
        for p in papers:
            if not isinstance(p, dict) or type(p.get("id")) is not int or p["id"] < 1 or p["id"] in ids or not isinstance(p.get("title"), str) or not p["title"].strip() or not isinstance(p.get("abstract"), str) or not p["abstract"].strip():
                parser.error("每篇论文需要唯一正整数 id 和非空 title / abstract。")
            ids.add(p["id"])
        base = args.base_url or input("GLM 兼容接口前缀（不要包含密钥）：").strip()
        model = args.model or input("模型名称：").strip()
        print(f"将发送 {len(papers)} 篇论文的标题/摘要，最多 {calls} 次调用；不自动重试。")
        print("本工具不写业务库，不与 Worker 共用额度计数；请自行控制两者合计费用。")
        if input("确认开始付费模型验收？输入 yes：").strip().lower() != "yes":
            print("已取消，未调用模型。")
            return 0
        key = getpass.getpass("API Key（隐藏输入，不保存）：")
        if not key.strip():
            print("密钥为空，未调用模型。", file=sys.stderr)
            return 1
        output = args.output.resolve() if args.output else root / "tmp" / ("ai-accept-" + __import__("datetime").datetime.now().strftime("%Y%m%d-%H%M%S"))
        request = {"key": key, "base_url": base, "model": model, "papers": papers,
                   "language": args.language, "groups": args.groups, "output": str(output), "mailpit": args.mailpit}
        # Never inherit an accidentally configured LLM_API_KEY into the child.
        env = {k: v for k, v in os.environ.items() if k != "LLM_API_KEY"}
        child = subprocess.run([str(binary)], input=json.dumps(request), text=True, env=env, cwd=root)
        if output.is_dir():
            print("报告目录：" + str(output).replace(key, "[REDACTED]"))
        # Python strings cannot be reliably zeroized; the process exits after this run.
        del request, key
        return child.returncode


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (KeyboardInterrupt, EOFError):
        print("\n验收中止，未完成的报告不能视为通过。", file=sys.stderr)
        sys.exit(130)
    except Exception:
        # No traceback: exception text can contain paths/configuration supplied by the user.
        print("验收未完成，请检查本地输入、文件权限和依赖。未输出密钥或底层异常内容。", file=sys.stderr)
        sys.exit(1)
