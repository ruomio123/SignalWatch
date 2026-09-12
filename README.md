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
- 论文详情点击「AI 论文助手」，选择模型后点击「生成论文报告」。按问题、方法、实验、结果、局限五项展示，可展开证据、复制 JSON/Markdown。当前会话成功生成报告后才能追问；报告失败、取消或论文版本变化时需先生成有效报告。打开面板和切换模型不会自动调用。
- 对话记录保存到服务端，刷新后可恢复。界面显示进度，也可停止本轮；模型失败不会自动重复调用。

全文解析需要 Worker 主机安装 Poppler 的 `pdftotext`、`pdfinfo`、`pdfimages` 和 util-linux 的 `prlimit`。首次解读按需准备文字全文并识别章节；全文获取或解析失败时自动生成显著标记的摘要版。OCR 本期仅预留接口，扫描页或文字提取不完整时降级；图片、公式和复杂表格可能无法可靠表达。摘要版追问继续只依据摘要，重新生成全文报告可再次尝试解析。

现有环境升级需停止 API/Worker、备份后应用至迁移 00031，并同步发布服务和前端。旧版未完成论文任务会明确终止，历史记录保留。报告通常 6 次模型调用，长文逐批读取，最多 24 次、15 分钟；追问最多 3 次、180 秒。订阅助手仍为 4 次模型调用、3 次工具、180 秒。日限额按实际调用计数，报告与追问共用 `AI_PAPER_QA_DAILY_LIMIT`。

架构与恢复说明见 [Agent 架构](docs/agent-architecture.md)。

论文助手全文校验排查与修复：PDF 使用阅读顺序解析，双栏正文不再按物理版式交错；模型只选择服务端提供的原文片段编号，引文与来源元数据由服务端读取，避免模型重抄 PDF 文字导致定位失败。前端「失败详情」显示步骤、规则和诊断编号。当前工作流为 `paper-fixed-v5`、解析器为 `poppler-reading-order-v3`，需同步更新 API、Worker 与前端；无需新增数据库迁移，旧缓存自动失效，历史报告保留。验证范围与真实调用的排查记录见 [Agent 架构与运维](docs/agent-architecture.md)。

论文输出结构由 Go 输出类型统一生成 JSON Schema，Qwen3.8 的论文任务使用原生严格 Schema；其他供应商接收同一 Schema 提示并由服务端校验。失败时可查看持久化的校验位置和规则，失败后仍显示实际依据范围。已有历史错误无法补回当时未保存的细节；升级不需要新增迁移。

论文报告语言：五项解释统一要求简体中文，模型/数据集名称、缩写、公式和原文证据可保留原文。整条无中文的报告论断以 `output_language_mismatch` 终止，不自动翻译或重试。追问按用户原问题的语言回答；已有报告保留原样，重新生成才应用当前规则。

论文双面板支持拖动分隔线调节大小、左右/上下排列、交换位置和恢复默认；布局在当前浏览器跨论文保存，小屏自动切换标签页。分隔线支持方向键、Shift 加速、Home/End 和双击均分。布局变化保留聊天草稿、会话和运行状态。新报告消息显示“快速了解论文”，历史消息保留原样。交互及截图见 [UI 验收记录](web/UI-REVIEW.md)。无需数据库迁移；使用内嵌前端时需重新构建并重启 API 才能生效。

订阅页也使用同一分栏组件，支持相同的拖动、键盘、方向与顺序调整。打开助手后列表与对话独立滚动，空间不足时切换「订阅列表 / AI 助手」标签页；关闭助手恢复列表页面滚动并返回打开按钮。订阅偏好保存于 `signalwatch.subscription-layout.v1`，与论文偏好独立；调整布局保留筛选、分页、输入及未保存草案，创建订阅仍需明确确认。

API 管理的「用量与调用记录」统一了字体层级、内容内边距和筛选布局，手机上筛选纵向排列、表格在自身区域横向滚动，长模型名称与诊断编号可换行。本次订阅布局和用量样式更新仅涉及前端，无新增接口、迁移或工作流版本。

## 移除系统运维角色

迁移 `00031_remove_user_roles.sql` 移除账号角色字段，原运维账号按普通账号使用，保留账号 ID、密码、启用/禁用状态、会话及业务数据。登录后的接口仍实时检查账号有效性，资源归属和任务权限检查继续生效。`/api/v2/ops/status` 与 `/api/v2/ops/sources` 已移除并返回 JSON 404，角色授予、撤销和查询命令已删除。

健康检查、日志及 Worker 的 Redis 状态快照保持可用；任务恢复继续使用 `go run ./cmd/ops retry digest --id ID` 和 `go run ./cmd/ops retry backfill --id ID`。

已有环境应停止 API/Worker，备份数据库后应用迁移 00031，再启动匹配的新版本，避免旧代码继续查询已删除的角色列。本次变更无需调整 Agent 工作流版本或重新生成报告。Down 只恢复角色列、约束和索引，全部账号默认为 `user`；若要恢复旧运维身份，必须使用升级前备份。
