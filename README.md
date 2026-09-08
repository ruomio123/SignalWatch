# SignalWatch

SignalWatch 提供内嵌 Web 前端、API、Worker，以及本地开发所需的 MySQL、Redis 和 Mailpit 服务。

## 当前功能（M3）

- Web 欢迎页、注册、登录、工作台与偏好设置页面
- 邮箱注册和密码登录
- 注册密码至少 8 个字符，且必须同时包含字母和数字
- JWT Bearer 鉴权
- 读取与修改用户时区、摘要时间和单次条目上限
- 查询系统启用的来源、允许分类及来源能力
- 创建、分页查询、修改、启停和软删除用户订阅
- 单分类与关键词订阅归一化、完整替换、版本冲突检查和启用额度保护
- API、MySQL 和 Redis 健康状态展示
- 订阅驱动、分类级共享的 arXiv 抓取
- 每小时无状态执行，并固定重复抓取最近 48 小时
- 基于 Redis Server Time 的全 Worker 三秒 arXiv 请求间隔
- arXiv ID 单行论文模型，v2/v3 直接覆盖并保留 first_seen_at
- 全局 Collector 锁、有限 HTTP 重试和 papers UPSERT 幂等
- Collector 每页落库后把新增和更新论文都提交给 Matcher
- 有界内存队列、固定 Matcher Worker Pool 和队列满时的生产者背压
- 交叉分类、标题/摘要关键词 OR 和 from-now 的确定性匹配
- subscription_papers 唯一约束幂等，并保留首次匹配原因与时间
- 当前用户匹配论文列表与详情 API，按论文去重并支持订阅筛选
- 匹配论文工作区、完整摘要详情和多订阅命中原因展示
- OpenAPI 3.1 接口契约和可重复的 M1/M2/M3 集成验收

M3 已打通后台的
`active categories -> arXiv updated 倒序分页 -> 本地 48h 截断 -> 每页 papers UPSERT -> 有界队列 -> subscription_papers`
链路，并提供只读的匹配论文查询体验。当前**尚未实现**邮件投递、用户反馈、LLM 或自然语言订阅助手；这些能力
不得视为已交付功能。

Worker 不会全量轮询全部 arXiv 分类。它在每轮开始时直接查询启用且未删除的订阅，
汇总并去重其分类；没有活跃分类时不请求 arXiv。关键词始终留在本地，M2 不把
用户关键词发送给 arXiv，也不保存抓取目标、运行记录或 checkpoint。

Matcher 只检查同一来源下仍启用且未删除的订阅。论文的任一交叉分类与订阅分类相同即
满足分类条件；空关键词表示仅按分类匹配，否则在标题和摘要拼接文本中按大小写不敏感
的 OR 语义查找关键词。作者和分类文本不参与关键词搜索。只有
`paper.first_seen_at >= subscription.created_at` 才会建立关系，因此新建订阅不会追溯
历史论文。

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

Worker 启动后立即执行一次 Collector，此后按 `COLLECTOR_INTERVAL` 再次运行。每轮固定
回看 `ARXIV_LOOKBACK`，默认 48 小时，并依赖 `(source_id, arxiv_id)` 唯一约束重复
UPSERT。每页 UPSERT 返回的全部论文 ID 都会进入 Matcher 队列，包括已存在论文；队列
由 `MATCHER_QUEUE_CAPACITY` 限制，`MATCHER_WORKERS` 个 Worker 并发消费。队列满时
Collector 会等待，从而形成背压。进程退出时未完成的内存任务由下一轮 48 小时重抓恢复。
Redis 不可用时不会绕过全局锁和限速继续访问 arXiv。

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

# Worker 完成抓取与匹配后，读取按论文去重的结果和完整详情
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

## M1/M2/M3 集成验收数据库

集成测试使用真实 Router、JWT、Service、Repository 和 MySQL。为防止误删开发数据，
它只接受数据库名以 `_test` 结尾的 `M1_TEST_MYSQL_DSN`，不会回退读取
`MYSQL_DSN`。先由数据库管理员创建一个独立测试库并授予测试账号权限，例如
`signalwatch_test`，然后在 `.env` 中配置：

```dotenv
M1_TEST_MYSQL_DSN=signalwatch:replace-me@tcp(127.0.0.1:3306)/signalwatch_test?charset=utf8mb4&parseTime=true&loc=UTC
```

不要把真实密码或本地 `.env` 提交到版本库。对独立测试库执行迁移和验收：

```bash
make migrate-test-up
make migrate-test-status
make test-integration
```

集成测试使用带随机后缀的隔离数据，只清理本次测试生成的记录；不会清空数据库或操作
开发库。它覆盖注册登录、资料、来源能力、订阅 CRUD、用户隔离，以及活跃分类汇总、
无需求时停止抓取、papers 幂等、arXiv 更新覆盖语义，以及 M3 匹配规则、from-now、
禁用/删除过滤、首次原因保留和并发幂等。
匹配论文 API 的验收还覆盖分页、按订阅筛选、同一论文跨订阅去重，以及严格的用户数据隔离。

## 常用检查

```bash
make fmt
make vet
make test
make test-race
make openapi-check
```

配置好独立测试数据库后，可以运行当前 M3 全量封板检查：

```bash
make m3-verify
```
