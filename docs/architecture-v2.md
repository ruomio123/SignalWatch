# 架构、升级与恢复

本次实现采用模块化单体，API 与 Worker 分进程部署。MySQL 是任务及投递事实的唯一来源；Redis 负责 arXiv 限速和按实例上报的运行状态。所有新增迁移位于 00019–00024，已有历史迁移保持原样。

## 模块和依赖

| 模块 | 唯一职责与入口 |
|---|---|
| `internal/rules` | 单分类、关键词 OR 的纯匹配规则；实时 Matcher 和历史 Backfill 共用 |
| `internal/subscription` | 参数归一化、额度和乐观版本检查；`commands.go` 编排短事务，Repository 负责存储 |
| `internal/backfill` | 固定窗口、游标分批、持久进度、失败恢复；`service.go` 不依赖数据库框架 |
| `internal/digest` | 候选读取、内容渲染、唯一每日投递状态机；只有 DeliveryStore 更新投递事实 |
| `internal/ai` | `SummaryQueries` 读写摘要需求；`DemandScanner` 扫描邮件需求；`Executor` 执行；`ConfigurationService` 管理凭据生命周期；MySQL 适配器保存状态和用量 |
| `internal/generation` | 与供应商无关的生成结果、错误和模型目录类型 |
| `internal/platform/llm` | 供应商目录及协议适配；客户端由启动层注入配置用例与执行器共用的工厂 |
| `internal/collector`、`internal/paper` | 来源采集、可续租的带代次锁、论文更新及 checkpoint |
| `internal/bootstrap`、`cmd/*` | 外部资源创建、依赖装配和进程生命周期 |
| `internal/operations` | 读取业务与按 Worker 实例保存的状态；人工写操作通过明确的本地 CLI 执行 |
| `web/src` | React 页面、类型化请求、会话、请求作用域、弹窗与页面错误边界 |

架构检查脚本阻止领域/应用用例导入 Gin、GORM、Redis、数据库连接或供应商适配器，以及在业务用例中构造 Repository。接口按使用方需要定义，不要求只读查询为联表读取引入额外层级。

配置创建/变更/删除、账户偏好和订阅额度命令共享账户行锁，顺序为账户 → 配置/订阅。外部模型调用在数据库事务外完成；保存时再次比较版本。启用 AI 时在事务内确认配置仍存在，防止校验后配置被删除的竞争。

## 数据与状态不变量

- 配置 `generation` 每个生命周期重新生成。用户修订计数器在配置删除后保留，任务身份和缓存同时携带 generation 与 revision；旧 ETag 和旧任务不能操作新配置。
- 订阅创建只保存订阅与回填任务。回填窗口冻结为创建时刻前七天，论文必须在创建前已入本地库，每批最多 200 篇。每次提交同时保存匹配与游标。租约 30 秒，批次预算 20 秒；过期执行者不能提交。规则变化或订阅停用会取消旧任务，已提交结果保留。
- 回填五次批次失败后进入 `failed`。重试保留游标。`matched` 是此回填评估中匹配的论文数，可能包括同时被实时 Matcher 写入的记录；唯一约束阻止重复关系。
- Digest 的唯一键是 `(subscription_id, local_date)`。候选论文、收件地址、主题、文本、HTML 和 Message-ID 一旦冻结就不再重选。空候选也完成当天任务。
- Digest：`pending → sending → sent/empty`；失败进入 `retry`，一分钟起指数退避，上限十五分钟；第五次失败或第五次租约过期进入 `failed`。人工重试重置尝试次数，保留原快照。暂停或删除订阅会在处理前取消任务，释放其论文占用。
- SMTP 确认成功到 MySQL 提交之间无法实现跨系统原子提交。不确定结果自动重试，接受可能重复；稳定 Message-ID 便于排查，不能保证收件端去重。
- 未完成和失败任务的论文占用保留，后续日期不能再次选择这些论文。成功任务同时更新 `delivered_at` 与任务状态，Redis 不参与正确性判定。
- Collector 使用数据库时间续租。写论文或推进 checkpoint 时验证 owner、epoch、expiry，并在写事务内锁住租约行。失去租约会取消请求；旧执行者的写入被拒绝。论文版本时间和 checkpoint 不倒退，匹配确认后才推进 checkpoint。

## 运行预算与状态

HTTP 头部读取 5 秒、正文读取 15 秒、写响应 45 秒、空闲连接 60 秒，请求处理上下文 35 秒；统一正文上限 64 KiB，凭据接口进一步限制大小。SIGINT/SIGTERM 后停止接收新请求，最多等待 15 秒关闭；Worker 取消调度并等待后台 goroutine 退出。

邮件处理预算 30 秒、租约 60 秒；AI 执行预算 25 秒、供应商调用最多 20 秒。邮件缓存增强最多读取 200 毫秒，失效或超时会降级为原邮件。邮件计数区分发送、完成跳过、租约竞争、失败和重试；AI 队列数据来自 Worker 实例，而不是 API 进程内的空队列。

## 切换步骤

1. 保留当前可运行二进制、静态产物、配置和主密钥版本；停止 API 与所有 Worker，等待在途请求退出。
2. 在停写状态下备份 MySQL，并保存行数、迁移版本、订阅数量及 `subscription_papers` 数量。确认备份能在隔离库恢复。凭据主密钥单独安全备份，不能提交到仓库。
3. 使用现有连接配置执行 `goose -dir migrations mysql "$MYSQL_DSN" up`。00019 扩展并回填配置身份；00020 扩展任务身份、失效旧 AI 任务；00021–00023 新增持久回填、邮件任务和采集租约表；00024 建立回填游标索引。
4. 核对用户、订阅、论文和匹配关系行数。核对现存 AI 配置 generation 非空，计数器 revision 不小于配置版本；旧任务不得仍为 pending/processing。
5. 构建前端并编译新 API/Worker。先启动 API，验证登录、订阅读写与 `/readyz`；再启动 Worker，观察回填、租约及邮件任务状态。
6. 使用同一版本发布前后端和 Worker。此次不提供 `/api/v1` 双版本兼容；浏览器重新载入后使用 `/api/v2`。API 配置冲突需重读 ETag 后重试。

MySQL 多条 DDL 不提供整组事务回滚。00019、00020、00022 明确禁止 Down；即使其他扩展表可删除，也不应将逐条 Down 当作本次部署的回退方案。DDL 中途失败时保持停写，保存失败日志和实际 schema，恢复经过核验的停写前备份，再用一致的迁移文件重试。不要手工把失败迁移标记为成功。

`scripts/test-migrations.sh` 在独立临时数据库演练空库初始化、从 00018 升级、00020 首条 DDL 后故障、备份恢复、重新升级及用户订阅/配置数据核对。它不读取业务 MYSQL_DSN。演练证明程序和恢复步骤可执行，不能代替生产备份验证。

## 查看失败与人工重试

操作员先确认故障原因已解决，再查询以下持久状态：

```sql
SELECT id, subscription_id, local_date, state, attempts, failure_code
FROM digest_deliveries WHERE state IN ('failed','retry') ORDER BY updated_at;
SELECT subscription_id, state, cursor_id, processed, matched, attempts, failure_code
FROM subscription_backfills WHERE state IN ('failed','pending','processing');
```

在已正确加载部署环境变量的 shell 中执行：

```bash
go run ./cmd/ops retry digest --id 123
go run ./cmd/ops retry backfill --id 456
```

Digest 的 ID 是投递记录 ID；Backfill 的 ID 是订阅 ID。只允许重试 `failed`，命令不会清空快照、游标或已匹配记录。业务用户可以在订阅页看到回填状态与数量。

## 验收与容量边界

`./scripts/verify.sh` 执行 Go 静态检查、单元/竞态/真实数据库测试、真实 API 响应契约、TypeScript、格式检查、浏览器行为及前端构建。独立数据库、Redis、Mailpit 缺失会失败。CI 同步执行恢复演练并检查嵌入静态产物是否与源代码一致。

容量测试命令：

```bash
SIGNALWATCH_CAPACITY=1 go test -v -count=1 -run '^TestCapacityWorkload$' ./internal/integration
```

需要显式设置独立 `M1_TEST_MYSQL_DSN`。测试创建并清理 1,000 用户、每用户 20 个订阅、100,000 篇论文，测量全量调度、单任务完整回填、20,000 个持久邮件任务、队列等待和 Go 堆使用。SMTP 使用本地假发送器，大部分投递为空；它不验证真实邮件供应商吞吐、2 万个回填任务同时运行或生产磁盘性能。生产容量承诺必须在目标机器上补充混合负载与持续运行测试。
