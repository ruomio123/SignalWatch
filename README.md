# SignalWatch

SignalWatch 提供内嵌 Web 前端、API、Worker，以及本地开发所需的 MySQL、Redis 和 Mailpit 服务。

## 当前功能（V2）

- Web 欢迎页、注册、登录、工作台与偏好设置页面
- 邮箱注册和密码登录
- 注册密码至少 8 个字符，且必须同时包含字母和数字
- JWT Bearer 鉴权
- 读取与修改用户时区、摘要时间和单次条目上限
- 查询系统启用的来源、允许分类及来源能力
- 创建、分页查询、修改、启停和软删除用户订阅
- 单分类与关键词订阅归一化、完整替换、版本冲突检查和启用额度保护
- API、MySQL 和 Redis 健康状态展示
- 独立于用户订阅、由来源允许分类驱动的本地 arXiv 论文库
- Search API 七天 Bootstrap/停机 Recovery 与每日逐分类 Atom Feed 增量发现
- Daily Feed ID 经 Search API 批量补全，保持首版时间、作者和版本元数据语义
- 来源级持久化同步 checkpoint、24 小时 Recovery overlap 和失败重试
- 基于 Redis Server Time 的全 Worker 三秒 arXiv 请求间隔
- arXiv ID 单行论文模型，v2/v3 直接覆盖并保留 first_seen_at
- 全局 Collector 锁、有限 HTTP 重试和 papers UPSERT 幂等
- Collector 每页落库后把新增和更新论文都提交给 Matcher
- 有界内存队列、固定 Matcher Worker Pool 和队列满时的生产者背压
- 交叉分类、标题/摘要关键词 OR 和 from-now 的确定性匹配
- 新建启用订阅时原子回填本地最近七天论文，不访问 arXiv
- subscription_papers 唯一约束幂等，并保留首次匹配原因与时间
- 当前用户匹配论文列表与详情 API，按论文去重并支持订阅筛选
- 匹配论文工作区、完整摘要详情和多订阅命中原因展示
- 按用户 IANA 时区和当地 digest_time 调度每日摘要
- 用户级候选聚合、跨订阅论文去重、已投递排除和条数上限
- Redis 用户日期处理锁与每日完成标记，失败保留同日重试能力
- 有界 Mail Queue、固定 Mail Worker Pool 和队列满时的生产者背压
- UTF-8 纯文本/HTML 邮件，包含论文、订阅和关键词命中原因
- SMTP 成功后事务更新 delivered_at，失败不写完成标记
- OpenAPI 3.1 接口契约和可重复的 M1–M4/V2 集成验收

V2 在 M4 完整闭环上重构了采集层：

~~~text
系统级 arXiv Bootstrap / Recovery / Daily Feed
→ papers UPSERT
→ subscription_papers 本地匹配
→ 用户当地时间的每日 Digest
→ SMTP 邮件
→ delivered_at
~~~

当前**尚未实现**用户反馈、LLM 或自然语言订阅助手；这些能力不得视为已交付功能。

Worker 不再根据订阅决定抓取目标。它读取启用 arXiv 来源的 `allowed_categories`，即使
没有用户或启用订阅也会维护本地论文库。关键词始终留在本地，不会发送给 arXiv。
首次运行使用 Search API 回看七天；错过每日任务时从最后成功时间减去 24 小时恢复；
只有整轮抓取、UPSERT 和 Matcher 入队成功后才推进 `sources.last_successful_sync_at`。

Matcher 只检查同一来源下仍启用且未删除的订阅。论文的任一交叉分类与订阅分类相同即
满足分类条件；空关键词表示仅按分类匹配，否则在标题和摘要拼接文本中按大小写不敏感
的 OR 语义查找关键词。作者和分类文本不参与关键词搜索。
普通论文匹配仍要求 `paper.first_seen_at >= subscription.created_at`。新建启用订阅则会在
同一数据库事务内额外匹配本地 `published_at` 最近七天的论文，创建成功后可立即在
`/papers` 查看。修改规则或重新启用不会重新回填。

## 环境要求

本地开发需要安装：

- Go，版本以 `go.mod` 为准
- Docker
- Docker Compose
- Make
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
等待，从而形成背压。进程退出时未完成的内存任务由下一轮 Recovery 重抓恢复。Redis
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
用户当地时间达到 `digest_time` 后会进入有界邮件队列；失败任务会在当天后续调度中重试，
成功或当天没有候选时写入 Redis 日期完成标记。单封邮件按最早发现顺序选择最多
`max_items_per_digest` 篇，剩余论文保留到下一天。开发环境邮件默认投递到 Mailpit，访问
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

生产环境应使用真实 SMTP 地址和发件人，并根据服务商设置认证与 STARTTLS。密码只保存在
本地环境变量中，不提交到版本库。

API 启动后访问 [http://127.0.0.1:8080](http://127.0.0.1:8080) 即可使用 Web 前端。前端资源通过 Go `embed` 打包在 API 二进制中，无需安装 Node.js 或启动额外的开发服务器。
修改 `web/static` 后需要重新编译或重启 API，浏览器才能加载新嵌入的资源。

接口契约位于 [`api/openapi.yaml`](api/openapi.yaml)。所有受保护接口使用登录响应中的
Bearer JWT；所有时间戳按 UTC RFC3339 返回，用户摘要时间使用 `HH:mm`。

### Web 页面

| 路径 | 功能 |
| --- | --- |
| `/` | 产品欢迎页与能力说明 |
| `/login` | 登录 |
| `/register` | 注册并自动登录 |
| `/app` | 阅读偏好、活跃订阅概览与使用建议 |
| `/papers` | 浏览匹配论文、按订阅筛选并查看完整匹配原因 |
| `/settings` | 修改时区、摘要时间和条目上限 |
| `/subscriptions` | 创建、筛选、修改、启停和删除订阅 |

## 最短 API 演示

先完成依赖启动和迁移，再在另一个终端执行 `make api`。注册与登录：

```bash
curl -i -X POST http://127.0.0.1:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"alice@example.test","password":"change-me-now1"}'

curl -i -X POST http://127.0.0.1:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"alice@example.test","password":"change-me-now1"}'
```

从登录响应复制 `access_token`，并将来源列表返回的 arXiv `id` 填入
`SOURCE_ID`：

```bash
TOKEN='<access_token>'
SOURCE_ID='<arxiv source id>'

curl -i http://127.0.0.1:8080/api/v1/me \
  -H "Authorization: Bearer $TOKEN"

curl -i -X PATCH http://127.0.0.1:8080/api/v1/me \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"timezone":"Asia/Shanghai","digest_time":"09:30","max_items_per_digest":25}'

curl -i http://127.0.0.1:8080/api/v1/sources \
  -H "Authorization: Bearer $TOKEN"

curl -i -X POST http://127.0.0.1:8080/api/v1/subscriptions \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"source_id\":$SOURCE_ID,\"name\":\"Agent papers\",\"objective\":\"Track agent systems\",\"rules\":{\"categories\":[\"cs.AI\"],\"include_keywords\":[\"tool use\"]}}"

# 创建时会先回填本地七天论文；Worker 后续持续采集并匹配新论文
curl -i 'http://127.0.0.1:8080/api/v1/papers?page=1&page_size=20' \
  -H "Authorization: Bearer $TOKEN"

PAPER_ID='<matched paper id>'
curl -i "http://127.0.0.1:8080/api/v1/papers/$PAPER_ID" \
  -H "Authorization: Bearer $TOKEN"
```

详情响应会返回 `ETag: "1"`。修改和删除必须把当前值原样放入
`If-Match`，下面的 `SUBSCRIPTION_ID` 取自创建响应：

```bash
SUBSCRIPTION_ID='<subscription id>'

curl -i http://127.0.0.1:8080/api/v1/subscriptions/$SUBSCRIPTION_ID \
  -H "Authorization: Bearer $TOKEN"

curl -i -X PATCH http://127.0.0.1:8080/api/v1/subscriptions/$SUBSCRIPTION_ID \
  -H "Authorization: Bearer $TOKEN" \
  -H 'If-Match: "1"' \
  -H 'Content-Type: application/json' \
  -d '{"enabled":false}'

curl -i -X DELETE http://127.0.0.1:8080/api/v1/subscriptions/$SUBSCRIPTION_ID \
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
V2 还覆盖新建启用订阅的本地七天原子回填、暂停订阅不回填，以及未投递关系可供
后续 Digest 聚合。
匹配论文 API 的验收还覆盖分页、按订阅筛选、同一论文跨订阅去重，以及严格的用户数据隔离。
M4 验收使用本地 Mailpit，覆盖用户级聚合、跨订阅去重、已投递排除、数量上限、SMTP
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
