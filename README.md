# SignalWatch

SignalWatch 提供 API、Worker，以及本地开发所需的 MySQL、Redis 和 Mailpit 服务。

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

## 常用检查

```bash
make fmt
make vet
make test
make test-race
```
