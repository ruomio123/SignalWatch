# SignalWatch

论文订阅与每日邮件服务：按 arXiv 分类和关键词匹配论文，在网页阅读或通过邮件接收更新，可选接入用户自带 API Key 的 AI 论文解读与邮件导读。

技术栈：Go、MySQL、Redis、React / TypeScript。API 提供接口和内嵌前端，Worker 负责采集、匹配、AI 任务和邮件发送。

## 本地启动

需要 Go（版本见 [go.mod](go.mod)）、Docker Compose、Make；修改前端还需 Node.js 22 和 npm。

安装迁移工具，并在项目根目录创建配置（已有 `.env` 时不要覆盖）：

```bash
go install github.com/pressly/goose/v3/cmd/goose@v3.24.3
cp .env.example .env
```

确保 `goose` 在 `PATH` 中。按需修改 `.env` 中的数据库、Redis 密码及 `JWT_SECRET`；修改数据库密码时，同步更新 `MYSQL_DSN`。完整配置见 [.env.example](.env.example)，不要提交真实密钥。

```bash
make deps-up          # 启动 MySQL、Redis、Mailpit
```

首次启动需等待 MySQL 初始化完成，再执行迁移：

```bash
make migrate-up
make migrate-status
```

分别在两个终端启动服务：

```bash
make api             # 终端一
make worker          # 终端二
```

- 网页：<http://127.0.0.1:8080>，注册后创建订阅。
- 开发邮件：<http://127.0.0.1:8025>（Mailpit）。
- 就绪检查：<http://127.0.0.1:8080/readyz>。

Worker 首次启动会采集最近七天论文，匹配结果需要等待采集与回填完成。邮件按用户时区和发送时间调度，每个订阅独立设置篇数上限。

浏览器默认保持登录 7 天，短期访问凭证自动续期。默认邮件仅进入本地 Mailpit；发送真实邮件需配置 `SMTP_*`。

## 可选 AI 配置

默认关闭 AI。启用时先生成用于加密用户 API Key 的主密钥：

```bash
openssl rand -base64 32
```

将生成结果填入 `.env`（`<生成结果>` 替换为实际值）：

```dotenv
AI_ENABLED=true
AI_CREDENTIAL_KEYS=v1:<生成结果>
AI_CREDENTIAL_ACTIVE_KEY_VERSION=v1
AI_ENABLED_PROVIDERS=glm,qwen,deepseek,kimi,openai

AI_CONFIG_TEST_MIN_INTERVAL=10s
AI_GENERATION_MIN_INTERVAL=2s
AI_CONFIG_TEST_DAILY_LIMIT=0
AI_PAPER_DAILY_LIMIT=0
AI_DIGEST_DAILY_LIMIT=0
```

间隔必须带时间单位，如 `10s`；每日上限 `0` 表示不限，启用上限时按 UTC 日统计。API 与 Worker 使用相同配置，修改后重启两个进程。

在侧栏「API 管理」点击「新建 API Key」，填写名称、供应商、模型和已有 Key，再点击「验证并保存」。同一家供应商可保存多条，列表支持编辑、验证、设为默认和删除。编辑时密钥留空保留该条目的原 Key；验证失败保留原配置。主密钥用于服务端加密，不是供应商 Key，需妥善备份。

模型调用最多等待 30 秒，失败不自动重试。论文解读可手动重试；AI 导读失败时仍发送普通论文邮件。

## 开发与检查

前端开发（需同时运行 API，默认代理到 8080）：

```bash
npm --prefix web ci
npm --prefix web run dev
```

发布前端修改：

```bash
make frontend
```

随后重新编译或重启 API；前端产物嵌入 Go 程序，运行时无需额外启动前端服务器。

常用检查：

```bash
make vet
make test
make test-race
make openapi-check
npm --prefix web run typecheck
npm --prefix web run format:check
(cd web && npx playwright install chromium)
npm --prefix web test
```

Go 测试在未配置依赖时会跳过部分集成测试。数据库集成测试需单独设置 `M1_TEST_MYSQL_DSN`（库名以 `_test` 结尾），以及测试专用邮件、Redis 服务；不得指向业务数据库。

## 常用维护

| 操作 | 命令 / 说明 |
| --- | --- |
| 查看迁移状态 | `make migrate-status` |
| 应用新增迁移 | `make migrate-up` |
| 停止依赖服务 | `make deps-down`，保留数据卷 |
| 查看运维账号 | `make ops-list` |
| 授予运维角色 | `make ops-grant EMAIL=operator@example.com`，账号需先注册 |
| 撤销运维角色 | `make ops-revoke EMAIL=operator@example.com` |

升级时先停止 API 和 Worker，备份数据库及 AI 加密主密钥，再执行迁移并启动新版本；不要混用新旧 Worker。保留数据的环境不要随意执行 `migrate-down`。

生产部署使用 HTTPS、独立强密码和真实 SMTP。设置 `APP_PUBLIC_URL=https://你的域名` 可让邮件链接跳转到站内论文详情。SMTP 不确定结果会重试，因此邮件可能重复。

## 参考

- [API 契约](api/openapi.yaml)：公开接口统一使用 `/api/v2`。
- [架构与切换恢复手册](docs/architecture-v2.md)。
- [重构验收记录](docs/refactor-progress.md)。
- [前端 UI 预览](web/UI-REVIEW.md)。

## Agent 助手

在「API 管理」保存所需供应商 Key，可为同一家供应商创建多个具名条目。默认条目用于邮件与摘要，对话中的 API 条目和模型单独选择。

- 订阅页点击「AI 创建订阅」，描述研究兴趣，检查或编辑草案后确认创建。
- 论文详情点击「AI 全文对话」，选择模型并提问；回答附带页码与文字证据。
- 对话记录保存到服务端，刷新后可恢复。界面显示进度，也可停止本轮；模型失败不会自动重复调用。

全文解析需要 Worker 主机安装 Poppler 的 `pdftotext`、`pdfinfo` 和 util-linux 的 `prlimit`。首次解读按需准备文字全文；全文失败时可选择仅基于摘要。暂不识别图片、复杂表格和扫描件。

现有环境升级需备份后应用新增迁移 00027–00029，并同步重启 API 和 Worker。每轮最多 4 次模型调用、3 次工具执行、180 秒；新功能日限额见 `.env.example`。

架构与恢复说明见 [Agent 架构](docs/agent-architecture.md)。
