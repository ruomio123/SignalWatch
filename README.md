# SignalWatch

SignalWatch 提供内嵌 Web 前端、API、Worker，以及本地开发所需的 MySQL、Redis 和 Mailpit 服务。

## 当前功能

- Web 欢迎页、注册、登录、工作台与偏好设置页面
- 邮箱注册和密码登录
- JWT Bearer 鉴权
- 读取与修改用户时区、摘要时间和单次条目上限
- API、MySQL 和 Redis 健康状态展示
- 订阅、订阅规则和内容来源的数据表结构

订阅管理、内容采集、规则匹配和摘要投递目前尚未实现业务 API；前端工作台会明确标注这些能力的当前状态。

## 环境要求

本地开发需要安装：

- Go，版本以 `go.mod` 为准
- Docker
- Docker Compose
- Make
- curl，用于检查 HTTP 接口
- [goose](https://github.com/pressly/goose)，用于执行数据库迁移

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

### Web 页面

| 路径 | 功能 |
| --- | --- |
| `/` | 产品欢迎页与能力说明 |
| `/login` | 登录 |
| `/register` | 注册并自动登录 |
| `/app` | 账户摘要、服务状态与后端能力进度 |
| `/settings` | 修改时区、摘要时间和条目上限 |

## 常用检查

```bash
make fmt
make vet
make test
make test-race
```
