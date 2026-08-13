# SignalWatch

SignalWatch 的 M0 版本提供 API、Worker，以及本地开发所需的 MySQL、Redis 和 Mailpit 服务。

## 环境要求

本地开发需要安装：

- Go，版本以 `go.mod` 为准
- Docker
- Docker Compose
- Make
- curl，用于检查 HTTP 接口

## 创建本地配置

在项目根目录复制环境变量示例：

```bash
cp .env.example .env