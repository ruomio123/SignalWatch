# SignalWatch

SignalWatch 提供内嵌 Web 前端、API、Worker，以及本地开发所需的 MySQL、Redis 和 Mailpit 服务。


## 架构重构与升级

当前公开协议为 **`/api/v2`**，前端使用 React、TypeScript、Vite，构建产物仍由 Go 内嵌。订阅规则只接受 `{"category":"cs.AI","keywords":["agent"]}`。AI 配置的 ETag 格式为 `"generation:version"`；订阅继续使用数字版本 ETag。

现有部署升级前，请按 [切换与恢复手册](docs/architecture-v2.md) 停止旧 API/Worker、备份数据库和凭据加密主密钥，再应用 00019–00024。旧 AI 任务/缓存会失效；历史业务数据保留。**不要把新 Worker 与旧 Worker 同时接入数据库。**

验证结果及容量测量见 [重构验收记录](docs/refactor-progress.md)。历史 `document/` 下的阶段文档保留用于追溯；当前契约以 `api/openapi.yaml` 为准。

前端修改后执行 `make frontend`，再编译或启动 API。独立前端开发可运行 `npm --prefix web run dev`，请求代理到本地 8080 端口。模板风格改版说明、截图和浏览器验收入口见 [前端 UI 预览](web/UI-REVIEW.md)。

完整隔离验收（测试容器使用独立端口，不读取 `.env`）：

```bash
make test-deps-up
export M1_TEST_MYSQL_DSN='root:isolated-test-only@tcp(127.0.0.1:13306)/signalwatch_test?parseTime=true&loc=UTC'
export M4_TEST_SMTP_ADDR=127.0.0.1:11025
export TEST_REDIS_ADDR=127.0.0.1:16379
goose -dir migrations mysql "$M1_TEST_MYSQL_DSN" up
npm --prefix web ci
(cd web && npx playwright install chromium)
./scripts/verify.sh
./scripts/test-migrations.sh
```

`verify.sh` 缺少集成依赖会失败，普通 `go test` 仍允许跳过未配置的数据库测试。测试禁止 HTTP 访问真实 AI 供应商；真实调用只通过单独手动验收进行。

## 登录会话与自动续期

浏览器默认保持登录 **7 天**。`JWT_TTL=15m` 控制短期访问凭证，过期后前端自动调用 `/api/v2/auth/refresh` 并重试原请求；`AUTH_SESSION_TTL=168h` 控制登录会话的固定有效期，未设置时也默认 7 天。离开页面半小时、关闭浏览器或重启 API 都不会单独导致重新输入密码；7 天到期、主动退出或账户停用后需要重新登录。

已有环境需先执行 `make migrate-up` 应用新增 `00025_browser_sessions.sql`，再构建前端并重新编译、启动 API。升级前签发的旧凭证没有会话 Cookie，**升级后需要正常登录一次**，之后自动续期生效。此迁移只新增登录会话表，不改变用户、订阅或论文数据。

会话凭据存放在 host-only、HttpOnly、SameSite=Lax Cookie 中，MySQL 只保存其 SHA-256 摘要；生产环境 Cookie 使用 Secure，需要 HTTPS。Cookie 固定 7 天到期，刷新不延长或轮换 Cookie。`POST /api/v2/auth/refresh` 与 `POST /api/v2/auth/logout` 要求 `X-SignalWatch-Session: 1`，仅供同源请求，响应禁止缓存。

同一页面的并发失效请求共享一次续期；续期不会重新挂载页面或清空未保存表单。网络中断、超时及服务端临时错误保留会话并提供重试；退出失败会明确提示，成功后同步退出其他标签页。退出立即撤销当前浏览器的续期资格，其他设备不受影响；已签发的访问凭证仍最多存活到其短期到期时间。最后一次续期签发的凭证不会超过登录会话截止时间。

## 当前功能（V2 + 可选 AI 邮件增强）

- Web 欢迎页、注册、登录、工作台与偏好设置页面
- 邮箱注册和密码登录
- 注册密码至少 8 个字符，且必须同时包含字母和数字
- JWT Bearer 鉴权
- 数据库实时授权的 `user` / `operator` 互斥角色和本地运维账号管理 CLI
- 读取与修改用户时区、摘要时间和新订阅默认条目上限
- 查询系统启用的来源、允许分类及来源能力
- 创建、分页查询、修改、启停和软删除用户订阅
- 单分类与关键词订阅归一化、完整替换、版本冲突检查和启用额度保护
- API、MySQL 和 Redis 健康状态展示
- 独立于用户订阅、由来源允许分类驱动的本地 arXiv 论文库
- Search API 七天 Bootstrap/停机 Recovery 与每日逐分类 Atom Feed 增量发现
- Daily Feed ID 经 Search API 批量补全，保持首版时间、作者和版本元数据语义
- 来源级持久化同步 checkpoint、24 小时 Recovery overlap 和失败重试
- 基于 Redis Server Time 的全 Worker 三秒 arXiv 请求间隔
- arXiv ID 单行论文模型，仅接受不早于当前版本的更新，保留 first_seen_at
- 可续租、带代次且验证写权限的 MySQL Collector 租约，以及有限 HTTP 重试和 papers UPSERT 幂等
- Collector 每页落库后把新增和更新论文都提交给 Matcher
- 有界内存队列、固定 Matcher Worker Pool 和队列满时的生产者背压
- 交叉分类、标题/摘要关键词 OR 和 from-now 的确定性匹配
- 新建启用订阅时原子创建回填任务，后台分批匹配本地最近七天论文，不访问 arXiv
- subscription_papers 唯一约束幂等，并保留首次匹配原因与时间
- 当前用户匹配论文列表与详情 API，按论文去重并支持订阅筛选
- 匹配论文工作区、完整摘要详情和多订阅命中原因展示
- 按用户 IANA 时区和当地 digest_time 调度每日摘要
- 每个订阅每天独立一封邮件、独立论文上限与投递记录；跨订阅可分别收录同一篇论文
- MySQL 每订阅/当地日期唯一投递任务，冻结内容、持久化完成状态并支持跨日重试
- 有界 Mail Queue、固定 Mail Worker Pool 和队列满时的生产者背压
- UTF-8 纯文本/HTML 邮件，包含论文、订阅和关键词命中原因
- SMTP 成功后原子更新 delivered_at 与每日任务；不确定结果自动重试，可能重复
- Redis 多 Worker 心跳、队列/任务快照和只读受保护运维状态接口
- 使用用户自带供应商 API 的中文或英文论文解读：总述、贡献、方法、适用场景和原文依据
- 精简订阅邮件：多论文研究主题、标题、作者、Comments、Subjects、命中关键词和详情链接；AI 不可用时保留清单
- 加密的用户 AI 配置，以及订阅级 Digest 总结开关和语言；详情页按需生成
- 用户维度的持久任务、租约恢复、调用并发限制和近 30 天 token 用量
- OpenAPI 3.0.3 接口契约和可重复的 M1–M4/V2 集成验收

V2 在 M4 完整闭环上重构了采集层：

~~~text
系统级 arXiv Bootstrap / Recovery / Daily Feed
→ papers UPSERT
→ subscription_papers 本地匹配
→ 用户当地时间的每订阅每日 Digest
→ SMTP 邮件
→ delivered_at
~~~

AI 功能默认关闭。当前实现仅处理标题与摘要；自然语言订阅、论文问答、订阅建议、PDF 和用户反馈尚未实现。真实模型质量验收由用户在页面中提交自己的供应商 Key，Key 不进入仓库或验收报告。

Worker 不再根据订阅决定抓取目标。它读取启用 arXiv 来源的 `allowed_categories`，即使
没有用户或启用订阅也会维护本地论文库。关键词始终留在本地，不会发送给 arXiv。
首次运行使用 Search API 回看七天；错过每日任务时从最后成功时间减去 24 小时恢复；
只有整轮抓取、UPSERT 和 Matcher 批次处理全部成功后才推进 `sources.last_successful_sync_at`。

Matcher 只检查同一来源下仍启用且未删除的订阅。论文的任一交叉分类与订阅分类相同即
满足分类条件；空关键词表示仅按分类匹配，否则在标题和摘要拼接文本中按大小写不敏感
的 OR 语义查找关键词。作者和分类文本不参与关键词搜索。
普通论文匹配仍要求 `paper.first_seen_at >= subscription.created_at`。新建启用订阅则会在
创建事务中保存最近七天的回填窗口，Worker 每批处理 200 篇。订阅响应中的 `backfill`
显示状态和进度；结果随批次提交出现在 `/papers`。回填途中暂停、删除或修改规则会取消
尚未完成的旧回填；已匹配记录保留。修改规则或重新启用不会重新回填。

## 环境要求

本地开发需要安装：

- Go，版本以 `go.mod` 为准
- Docker
- Docker Compose
- Make
- Node.js 22 与 npm，用于修改和构建 React/TypeScript 前端
- curl，用于检查 HTTP 接口
- [goose](https://github.com/pressly/goose)，用于执行数据库迁移

OpenAPI 校验由 Go 测试中的 `kin-openapi v0.133.0` 完成，不需要额外安装全局
Swagger 工具。

## 本地启动

在项目根目录创建本地配置：

```bash
cp .env.example .env
```

按顺序启动依赖并执行全部迁移：

```bash
make deps-up
make migrate-up
make migrate-status
```

如果曾使用修复前的 00017，并遇到
`Cannot drop index 'uk_paper_ai_summaries_identity': needed in a foreign key constraint`，
不要执行 `make migrate-down`：goose 尚未登记 00017，回滚会误操作 00016。本地 Compose
数据库可先执行下面的精确状态修复，再重新迁移：

```bash
make repair-00017-partial
make migrate-up
make migrate-status
```

修复脚本会检查 00017 尚未登记、三个新表为空、列与旧索引均符合该次失败留下的状态；
任一条件不符都会停止，不继续修改数据库。

`migrate-down` 每次只回滚最近的一个迁移版本，适合一次性验收数据库结构；不要在保留开发数据的数据库上执行回滚。

```bash
make migrate-down
```

启动 API 或 Worker：

```bash
make api
make worker
```

Worker 启动后读取来源 checkpoint：空 checkpoint 执行七天 Bootstrap，落后于最近每日
边界则执行带 24 小时 overlap 的 Recovery，否则等待 Daily。Daily 默认在
`00:30 America/New_York` 逐分类读取 Atom Feed，再通过 Search API `id_list` 补全元数据。
失败任务每 15 分钟保持原模式重试。系统依赖 `(source_id, arxiv_id)` 唯一约束重复 UPSERT。
每批 UPSERT 返回的全部论文 ID 都会进入 Matcher 队列，包括已存在论文；队列由
`MATCHER_QUEUE_CAPACITY` 限制，`MATCHER_WORKERS` 个 Worker 并发消费。队列满时采集器会
等待，从而形成背压。Collector 会等待当前页的匹配结果；匹配失败或取消时不推进
checkpoint，下一次同步按 Bootstrap/Recovery 重抓并幂等重试。Redis
不可用时不会绕过全局锁和限速继续访问 arXiv。

```dotenv
COLLECTOR_LOCK_TTL=55m
ARXIV_BOOTSTRAP_LOOKBACK=168h
ARXIV_RECOVERY_OVERLAP=24h
ARXIV_DAILY_SYNC_TIME=00:30
ARXIV_SYNC_RETRY_INTERVAL=15m
ARXIV_FEED_ENDPOINT=https://rss.arxiv.org/atom
```

同一个 Worker 启动时也会立即执行一次 Digest 调度，此后按 `DIGEST_INTERVAL` 检查。
Digest 调度串行运行在独立循环中，邮件队列背压不会阻塞 Worker 心跳。
用户当地时间达到 `digest_time` 后，各启用订阅分别进入有界邮件队列；失败任务按退避策略继续重试（可跨日），
成功或当天没有候选时在 MySQL 中完成当天任务。每封邮件按最早发现顺序选择最多
该订阅的 `max_items_per_digest` 篇，剩余论文保留到下一天。开发环境邮件默认投递到 Mailpit，访问
[http://127.0.0.1:8025](http://127.0.0.1:8025) 查看。

SMTP 默认配置适用于本地 Mailpit：

```dotenv
DIGEST_INTERVAL=1m
DIGEST_LOCK_TTL=10m
DIGEST_COMPLETION_TTL=72h
MAIL_WORKERS=2
MAIL_QUEUE_CAPACITY=128
SMTP_ADDR=127.0.0.1:1025
SMTP_FROM=SignalWatch <digest@signalwatch.local>
SMTP_USERNAME=
SMTP_PASSWORD=
SMTP_STARTTLS=false
SMTP_TIMEOUT=10s
```

运维状态快照默认在 Redis 保留七天；配置不得短于一小时：

```dotenv
OPS_STATUS_RETENTION=168h
```

生产环境应使用真实 SMTP 地址和发件人，并根据服务商设置认证与 STARTTLS。密码只保存在
本地环境变量中，不提交到版本库。

API 启动后访问 [http://127.0.0.1:8080](http://127.0.0.1:8080) 即可使用 Web 前端。前端资源通过 Go `embed` 打包在 API 二进制中，无需安装 Node.js 或启动额外的开发服务器。
修改 `web/src` 后先执行 `make frontend` 生成 `web/static`，再重新编译并启动 API；已运行的旧二进制不会自动加载新资源。

接口契约位于 [`api/openapi.yaml`](api/openapi.yaml)。所有受保护接口使用登录响应中的
Bearer JWT；所有时间戳按 UTC RFC3339 返回，用户摘要时间使用 `HH:mm`。

### 当前可靠性边界

- Collector 和 Digest 使用固定 TTL 的 Redis 锁，目前不续租；部署时应确保单轮任务耗时小于
  对应锁 TTL，超时场景不承诺跨 Worker 互斥。
- SMTP 成功与 MySQL 投递标记不属于同一事务；邮件已被 SMTP 接收但投递标记写入失败时，
  后续重试可能重复发送。SMTP 接收成功也不代表收件方最终送达。
- 当天没有候选会写每日完成标记，之后新匹配的论文保留到下一天。

## 系统运维角色

公开注册始终创建普通 `user`，请求体中的 `role` 会被拒绝。运维人员复用登录接口获取
JWT；JWT 只保存用户 ID，每次请求都会从 MySQL 读取当前账号状态和角色，因此授权、撤权或
停用账号会立即生效。`operator` 只能访问 `/api/v2/ops/*`，不能使用资料、订阅、来源和论文
业务接口，也不会进入 Digest 调度。普通 `user` 访问运维接口会得到 `403 AUTH_FORBIDDEN`。

先注册一个专用账号，再由本机执行角色管理命令：

```bash
make ops-grant EMAIL=operator@example.com
make ops-list
make ops-revoke EMAIL=operator@example.com
```

授权和撤权命令幂等，只操作已存在账号；非活跃账号不能被授权，最后一个活跃 operator
不能被撤销。每次变更和列表查询都输出结构化审计日志。部署时必须先运行
`make migrate-up` 添加 `users.role`，再启动新版 API/Worker 并授权专用账号。

登录后可读取两个只读接口：

```bash
OPS_TOKEN='<operator access_token>'

curl -i http://127.0.0.1:8080/api/v2/ops/status \
  -H "Authorization: Bearer $OPS_TOKEN"

curl -i http://127.0.0.1:8080/api/v2/ops/sources \
  -H "Authorization: Bearer $OPS_TOKEN"
```

`/api/v2/ops/status` 汇总 MySQL/Redis 检查耗时、Worker 在线状态、Matcher/Mail 队列、
Collector/Digest/Matcher/Mail 最近任务状态和来源 checkpoint 汇总。没有在线 Worker、缺失或
失败的任务状态、来源未同步/落后，或队列使用率达到 80% 时会显示 `degraded`。组件故障会
尽量以 `200` 返回 `unavailable` 状态；负载均衡仍应使用公开 `/readyz` 的 `503` 判断 API
能否接流量。

`/api/v2/ops/sources` 返回 arXiv 来源允许分类、论文统计、同步 checkpoint、纽约每日边界下
的 `current/stale/never_synced` 状态和安全的最近尝试摘要。接口不会返回原始
`config_json`、Redis 锁值、连接串、SMTP 凭证或完整内部错误。系统不提供运维后台页面、
日志下载、手动同步、重试、取消或发信接口；部署环境负责采集 JSON stdout。

### Web 页面

| 路径 | 功能 |
| --- | --- |
| `/` | 产品欢迎页与能力说明 |
| `/login` | 登录 |
| `/register` | 注册并自动登录 |
| `/app` | 阅读偏好、活跃订阅概览与使用建议 |
| `/papers` | 浏览匹配论文、按订阅筛选并查看完整匹配原因 |
| `/settings` | 修改时区、发送时间和新订阅默认上限 |
| `/subscriptions` | 创建、筛选、修改、启停和删除订阅 |

## 最短 API 演示

先完成依赖启动和迁移，再在另一个终端执行 `make api`。注册与登录：

```bash
curl -i -X POST http://127.0.0.1:8080/api/v2/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"alice@example.test","password":"change-me-now1"}'

curl -i -X POST http://127.0.0.1:8080/api/v2/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"alice@example.test","password":"change-me-now1"}'
```

从登录响应复制 `access_token`，并将来源列表返回的 arXiv `id` 填入
`SOURCE_ID`：

```bash
TOKEN='<access_token>'
SOURCE_ID='<arxiv source id>'

curl -i http://127.0.0.1:8080/api/v2/me \
  -H "Authorization: Bearer $TOKEN"

curl -i -X PATCH http://127.0.0.1:8080/api/v2/me \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"timezone":"Asia/Shanghai","digest_time":"09:30","max_items_per_digest":25}'

curl -i http://127.0.0.1:8080/api/v2/sources \
  -H "Authorization: Bearer $TOKEN"

curl -i -X POST http://127.0.0.1:8080/api/v2/subscriptions \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"source_id\":$SOURCE_ID,\"name\":\"Agent papers\",\"objective\":\"Track agent systems\",\"rules\":{\"category\":\"cs.AI\",\"keywords\":[\"tool use\"]}}"

# 创建立即返回回填状态；Worker 后台回填并持续匹配新论文
curl -i 'http://127.0.0.1:8080/api/v2/papers?page=1&page_size=20' \
  -H "Authorization: Bearer $TOKEN"

PAPER_ID='<matched paper id>'
curl -i "http://127.0.0.1:8080/api/v2/papers/$PAPER_ID" \
  -H "Authorization: Bearer $TOKEN"
```

详情响应会返回 `ETag: "1"`。修改和删除必须把当前值原样放入
`If-Match`，下面的 `SUBSCRIPTION_ID` 取自创建响应：

```bash
SUBSCRIPTION_ID='<subscription id>'

curl -i http://127.0.0.1:8080/api/v2/subscriptions/$SUBSCRIPTION_ID \
  -H "Authorization: Bearer $TOKEN"

curl -i -X PATCH http://127.0.0.1:8080/api/v2/subscriptions/$SUBSCRIPTION_ID \
  -H "Authorization: Bearer $TOKEN" \
  -H 'If-Match: "1"' \
  -H 'Content-Type: application/json' \
  -d '{"enabled":false}'

curl -i -X DELETE http://127.0.0.1:8080/api/v2/subscriptions/$SUBSCRIPTION_ID \
  -H "Authorization: Bearer $TOKEN" \
  -H 'If-Match: "2"'
```

## M1–M4/V2 集成验收数据库

集成测试使用真实 Router、JWT、Service、Repository 和 MySQL。为防止误删开发数据，
它只接受数据库名以 `_test` 结尾的 `M1_TEST_MYSQL_DSN`，不会回退读取
`MYSQL_DSN`。先由数据库管理员创建一个独立测试库并授予测试账号权限，例如
`signalwatch_test`，然后在 `.env` 中配置：

```dotenv
M1_TEST_MYSQL_DSN=signalwatch:replace-me@tcp(127.0.0.1:3306)/signalwatch_test?charset=utf8mb4&parseTime=true&loc=UTC
M4_TEST_SMTP_ADDR=127.0.0.1:1025
```

不要把真实密码或本地 `.env` 提交到版本库。对独立测试库执行迁移和验收：

```bash
make migrate-test-up
make migrate-test-status
make test-integration
```

集成测试使用带随机后缀的隔离数据，只清理本次测试生成的记录；不会清空数据库或操作
开发库。它覆盖注册登录、资料、来源能力、订阅 CRUD、用户隔离，以及系统来源分类、
零订阅采集、同步 checkpoint、papers 幂等、arXiv 更新覆盖语义，以及 M3 匹配规则、from-now、
禁用/删除过滤、首次原因保留和并发幂等。
V2 还覆盖新建启用订阅的本地七天异步回填、暂停订阅不回填，以及未投递关系可供
后续 Digest 聚合。
运维验收覆盖角色默认值和约束、授权/撤权幂等、非活跃账号、最后一个 operator 保护、
实时角色路由隔离、Worker 状态降级规则、纽约夏令时和安全审计字段。
匹配论文 API 的验收还覆盖分页、按订阅筛选、同一论文跨订阅去重，以及严格的用户数据隔离。
M4 验收使用本地 Mailpit，覆盖订阅独立投递、跨订阅重复论文、独立数量上限、SMTP
失败重试、事务投递标记和同日幂等。运行验收前确保 `make deps-up` 中的 Mailpit 健康。

## 常用检查

```bash
make fmt
make vet
make test
make test-race
make openapi-check
```

配置好独立测试数据库后，可以运行当前 V2 全量封板检查：

```bash
make v2-verify
```

## 用户自带 API 的 AI 增强

先执行 `make migrate-up`（新增迁移 12–18），再发布默认关闭的 API / Worker。服务环境中只保存用于加密用户 Key 的主密钥，不配置供应商 Key：

```dotenv
AI_ENABLED=false
AI_CREDENTIAL_KEYS=
AI_CREDENTIAL_ACTIVE_KEY_VERSION=v1
AI_ENABLED_PROVIDERS=glm,qwen,deepseek,kimi,openai
AI_WORKERS=2
AI_QUEUE_CAPACITY=128
```

用 `openssl rand -base64 32` 生成主密钥，然后配置成 `AI_CREDENTIAL_KEYS=v1:<base64>`。
`AI_ENABLED=true` 时，API 和 Worker 会校验主密钥、活动版本及开放供应商；错误配置会使进程启动失败。
轮换时同时加载旧版本和新版本，例如 `v1:<old>,v2:<new>`，并把活动版本改成 `v2`。
主密钥不能进入仓库。数据库备份会保留历史密文，必须按数据库备份策略保护和清理。

供应商 endpoint 由服务端固定，用户不能填写 URL、Header 或任意模型。当前支持智谱 GLM、阿里云百炼
Qwen（中国站）、DeepSeek、Kimi（月之暗面）和 OpenAI API；`AI_ENABLED_PROVIDERS` 决定页面实际开放项。
模型清单采用适合摘要任务的精选白名单，停用模型会在页面提示切换，不再产生 AI 调用。
用户在「偏好设置」新增配置，选择白名单模型并输入 Key。服务先执行最小 JSON 测试，成功后用
AES-256-GCM 加密保存；页面和 API 以后只返回末四位。更换配置、切换模型和轮换 Key 都使用版本控制，
失败不会覆盖旧配置。删除配置会同时关闭论文解读和全部订阅 AI 开关。
生产环境必须在 SignalWatch 前终止 HTTPS，并禁止经明文公网 HTTP 访问配置接口；Key 只放在 JSON
请求体中，访问日志不记录请求正文或 Authorization Header。开发环境可继续通过本机回环 HTTP 调试。

论文详情只在用户点击后创建任务，不再后台批量预生成。默认不限制每日调用次数，缓存命中不计调用。
Digest 每五分钟查看未来三十分钟将发送的订阅，只为已单独启用 AI、配置有效且至少
两篇论文的邮件预生成一次。输入仅包含本封实际选中的最多 20 篇标题、摘要和论文 ID。
每个任务只调用供应商一次，不自动重试；401 凭据拒绝会把正在使用的配置标为无效；403 单独报告模型访问受限，429、5xx 和网络错误只使任务失败。
Worker 通过数据库租约确保一个用户同一时间最多进行一次模型调用。

AI 调用策略由 API 和 Worker 共用，默认配置如下（每日上限 `0` 表示不限）：

```dotenv
AI_CONFIG_TEST_MIN_INTERVAL=10s
AI_GENERATION_MIN_INTERVAL=2s
AI_CONFIG_TEST_DAILY_LIMIT=0
AI_PAPER_DAILY_LIMIT=0
AI_DIGEST_DAILY_LIMIT=0
```

配置验证与后台生成共用每用户单调用租约；论文与邮件共享生成间隔。间隔可配置为 1 秒至 1 小时，
每日上限范围 0～1000000。设置页以“验证并保存”提交当前表单，同供应商密钥留空会复用已有密钥。
有未保存修改时隐藏“测试当前配置”，避免测错模型。供应商调用最多等待 30 秒，不自动重试；
失败显示具体原因、诊断编号与可用的等待时间，倒计时结束不会再次调用。

用量按准入时的 UTC 日期归属，记录开始调用的尝试次数（并非供应商账单）。本地拦截和开始前取消不计调用；
失败及结果未知仍计入可选每日上限。token 用量仅使用供应商提供的数字，不估算费用。
新增 `/api/v2/ai/calls` 查询最近 30 天详细记录；历史汇总持续保留，升级前的汇总不会伪造详细记录。
Worker 每分钟恢复过期调用为结果未知，并清理过期详情；恢复只结算，不重发模型请求。

本次升级新增 `00026_ai_call_records.sql`。停下 API 与 Worker，备份数据库后执行 `make migrate-up`，
再使用相同策略配置重新编译并启动两个进程，避免新旧计数混用。已有配置和历史用量保持不变，
默认不限后无需清空旧计数。旧失败任务不会在迁移中批量重发，论文可手动重新请求；邮件保持降级策略。
迁移中断应停止启动新进程，恢复迁移前备份再执行；不要对部分成功 DDL 盲目运行 Down。
GLM 4.7 Flash 仍需另行授权真实调用验收；调整超时和输出预算不代表其原始故障已确认修复。

论文缓存按用户、论文 ID、标题/摘要哈希、语言、供应商、模型和配置版本识别，不依赖 `updated_at`。
旧平台结果标为 legacy 并停止展示；失效论文结果保留三十天，Digest 快照保留七天。
论文和 Digest 内容都只基于原始标题、摘要，原文证据必须逐字匹配；主题引用必须属于
本次论文集合。结构校验不能代替事实质量审查，单篇推断场景在详情页明确标记。

邮件在原有候选选定后，仅用总计 200ms 的读取预算查询缓存，不调用模型。
Digest 只使用用户、订阅、当地日期、语言、候选顺序/内容版本、时区与该订阅条数上限完全匹配的总结。
邮件不展开原始摘要或单篇 AI 解读，仅展示多篇总结的概览段落，以及每篇论文的标题、作者、
Comments、Subjects、命中关键词和链接。Comments 来自 arXiv 元数据；历史论文在再次采集前显示“未提供”。
概览中文最多显示 240 字符、英文 600 字符，超长以省略号标识节选，不展开主题明细。
缓存缺失、损坏或增强模板失败时省略总结，仍使用精简模板。
例如本订阅匹配 20 篇、上限 10 篇，本次仅对选出的 10 篇生成总结；另外 10 篇不标记已投递。
页脚按本订阅发送前的未投递记录统计剩余数量（排除本次论文）；统计失败时省略提示，不阻止发送。
可选配置 `APP_PUBLIC_URL=https://你的站点域名`（仅 origin，不带路径），然后重启 Worker，
邮件即可链接到站内论文详情和本订阅论文列表；未配置时论文链接指向 arXiv。
本地测试可设置 `APP_PUBLIC_URL=http://127.0.0.1:8080`，供其他设备使用时应换成其能访问的地址。
打开站内链接后如需登录，登录成功会返回目标页面。
迟到结果不补发邮件，论文数量、顺序和 SMTP 成功后的投递标记规则保持原有语义。

接口（JWT 普通用户权限，沿用匹配论文可见性）：

- `GET /api/v2/ai/providers`：返回当前开放供应商和白名单模型。
- `GET|PUT|PATCH|DELETE /api/v2/ai/configuration`：读取掩码状态、新增/更换、切换模型和删除配置。
- `PUT /api/v2/ai/configuration/secret`、`POST /api/v2/ai/configuration/test`：轮换 Key 和测试已保存配置。
- `GET /api/v2/ai/usage`：读取最多近 30 天按功能聚合的调用与 token 用量。
- `PATCH /api/v2/me`：更新论文详情的 `ai_enabled` 和 `ai_language`。
- `GET /api/v2/papers/:id/ai-summary?language=zh|en`：只读当前用户配置版本的缓存状态。
- `POST /api/v2/papers/:id/ai-summary`，JSON `{"language":"zh"}`：请求后台生成；已有结果 200，待生成 202。
- `GET /api/v2/ops/status`：增加 `ai` 状态和 Worker 的 `ai` 任务快照；AI 不可用不会单独影响 `/readyz`。

额度与 token 用量按用户、UTC 日期和功能跨 Worker 持久化；队列、成功/失败、缓存命中计数是
进程启动以来的本地统计。供应商未返回 usage 时只增加 `usage_missing`，不伪造 token 数，也不估算金额。

AI 确定性测试随 `make v2-verify` 执行。指定独立、已迁移且名称以 `_test` 结尾的
`M1_TEST_MYSQL_DSN`，并为 `M4_TEST_SMTP_ADDR` 配置独立 Mailpit。
真实模型验收另需测试用户至少三十篇摘要和十组多论文样本，逐项审查原文依据、缺失信息、
推断标识与样本范围，再在 Mailpit 查看实际邮件；未完成此步骤不能标记本阶段全部验收通过。

## 每个订阅独立发送邮件

每个启用且未删除的订阅，每个用户当地日期最多发送一封邮件。发送时间和时区仍沿用用户
设置；无待投递论文的订阅不发邮件。邮件标题和正文标明订阅名称。

- 每条订阅有独立 `max_items_per_digest`（1–20），在新建/编辑订阅时设置。
- 比如三个订阅上限为 5、10、20，各自按自己的上限选取论文，最多发送三封邮件。
- 同一篇论文命中两个订阅，可以分别出现在两封邮件中；A 的投递不会标记 B 为已投递。
- 单个订阅仍按本地首次发现时间和 ID 从早到晚选择未投递论文，超过上限的积压留待后续日期。
- 用户设置里的上限改为“新订阅默认上限”，只在创建时复制，之后改变默认值不会影响已有订阅。
- AI 论文解读按用户和配置版本隔离；多论文总结按每封订阅邮件独立生成和匹配。

`POST /api/v2/subscriptions` 可指定 `max_items_per_digest`，省略时继承用户的默认值。
`PATCH /api/v2/subscriptions/:id` 可单独修改该值，沿用 `If-Match` 版本校验。
迁移 16 为现有订阅复制各自用户的原有上限；迁移 17 把大于 20 的值压缩为 20，并关闭所有旧 AI 开关。

升级时先停止旧 Worker，再执行 `make migrate-up`，随后重启 API 和全部 Worker。
不要同时运行新旧版本 Worker：旧版本仍使用用户级聚合和投递更新逻辑。
每日完成事实保存在 MySQL；Redis 丢失不会触发当天重新选取下一批论文。
因此升级当天，如果已过设置时间且某个订阅仍有待投递论文，它可能在下个调度周期发送。
修改同一订阅的上限或发送时间，不会清除该订阅当天的完成标记或触发补发。

### 真实模型验收

在浏览器「偏好设置 → AI 模型配置」选择 GLM 和白名单模型，在密码框中输入测试 Key。
页面只把 Key 发送到 SignalWatch API，成功后输入框立即清空，接口只回显末四位。随后为测试订阅
启用 AI 总结并选择语言，等待发送窗口后在 Mailpit 核对邮件概览；论文详情解读需要单独启用并点击生成。
结构校验通过后仍需人工核查事实质量。验收完成可直接删除配置，删除操作会关闭所有 AI 开关。
