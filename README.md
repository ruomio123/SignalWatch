# SignalWatch

SignalWatch 提供内嵌 Web 前端、API、Worker，以及本地开发所需的 MySQL、Redis 和 Mailpit 服务。

## 当前功能

- Web 欢迎页、注册、登录、工作台与偏好设置页面
- 邮箱注册和密码登录
- 注册密码至少 8 个字符，且必须同时包含字母和数字
- JWT Bearer 鉴权
- 读取与修改用户时区、摘要时间和单次条目上限
- 查询系统启用的来源目录及来源能力
- 创建、分页查询、修改、启停和软删除用户订阅
- 订阅规则归一化、完整替换、版本冲突检查和启用额度保护
- API、MySQL 和 Redis 健康状态展示
- OpenAPI 3.1 接口契约和可重复的 M1 多用户集成验收

M1 中的 arXiv 只是已经迁移并 seed 的来源目录记录。当前**尚未实现** arXiv/RSS
内容拉取、内容修订、规则预览、匹配、邮件投递、LLM 或 Agent；这些能力不得视为
M1 已交付功能。

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
  -d "{\"source_id\":$SOURCE_ID,\"name\":\"Agent papers\",\"objective\":\"Track agent systems\",\"rules\":{\"categories\":[\"cs.AI\"],\"authors\":[],\"include_keywords\":[\"tool use\"],\"exclude_keywords\":[\"survey\"]}}"
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

## M1 集成验收数据库

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

集成测试使用唯一邮箱创建两个用户，只清理本次测试生成的用户、订阅、规则和禁用来源
fixture；不会清空数据库或操作开发库。它覆盖注册登录、资料、来源、订阅 CRUD、规则及
邮箱唯一性、20 条启用额度、版本冲突、软删除和跨用户隔离。

## 常用检查

```bash
make fmt
make vet
make test
make test-race
make openapi-check
```

配置好独立 M1 测试数据库后，可以运行完整封板检查：

```bash
make m1-verify
```
