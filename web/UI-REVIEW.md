# 前端 UI 改版与预览

本轮参考工作区 `admin-panel-template` 的视觉设计，统一为深灰侧栏、白色顶栏、浅灰背景和靛蓝控件。模板目录保持原样；保留 React、TypeScript、Vite 和 Go 内嵌发布。模板来源说明与授权见该目录的 README，图标使用 lucide-react。

## 查看截图

运行浏览器验收后执行 `npm --prefix web run review:gallery`，打开 [44 张截图总览](test-results/ui-review.html)，可切换 1440、768、390、320px 宽度，点击图片查看原尺寸。

| 页面 | 桌面 | 移动端 |
|---|---|---|
| 首页 | [1440px](test-results/ui-visual-review-and-long-content-at-1440px/home-1440.png) | [390px](test-results/ui-visual-review-and-long-content-at-390px/home-390.png) |
| 概览 | [1440px](test-results/ui-visual-review-and-long-content-at-1440px/dashboard-1440.png) | [390px](test-results/ui-visual-review-and-long-content-at-390px/dashboard-390.png) |
| 订阅 | [1440px](test-results/ui-visual-review-and-long-content-at-1440px/subscriptions-1440.png) | [390px](test-results/ui-visual-review-and-long-content-at-390px/subscriptions-390.png) |
| 论文 | [1440px](test-results/ui-visual-review-and-long-content-at-1440px/papers-1440.png) | [390px](test-results/ui-visual-review-and-long-content-at-390px/papers-390.png) |
| 设置 | [1440px](test-results/ui-visual-review-and-long-content-at-1440px/settings-configured-1440.png) | [390px](test-results/ui-visual-review-and-long-content-at-390px/settings-configured-390.png) |

截图使用固定 API 替身数据，涵盖登录、注册、长标题、多关键词、长邮箱、回填状态、编辑弹窗及长篇 AI 解读。它们用于 UI 评审，不代表真实账户或调用。截图和总览是忽略提交的测试产物，下次测试会重新生成。

## 布局与实现约定

- 工作台侧栏宽 224px；小于 768px 使用原生对话框抽屉，支持遮罩、Escape 和导航后关闭。
- 订阅使用六列表格；列表容器宽度不超过 1100px 时改为堆叠信息行，避免平板工作区过窄。论文使用紧凑阅读列表。
- 普通弹窗最大宽度 640px，论文详情 960px；保留键盘焦点约束、关闭后焦点恢复与独立内容滚动。
- `src/styles` 分为设计变量、组件、布局、页面四层；`Common.tsx` 提供公共展示组件。`lib/account.tsx` 统一读取账户，偏好保存及 AI 配置变更会刷新受影响视图。
- API、数据库和模板目录没有变更。保留版本 ETag、401/409/503 处理、过期请求取消及 AI 轮询取消。

## 验证与本地预览

使用 Node.js 22。在项目根目录执行：

```bash
npm --prefix web ci
(cd web && npx playwright install chromium)
npm --prefix web run typecheck
npm --prefix web run format:check
npm --prefix web test -- --workers=4
npm --prefix web run review:gallery
npm --prefix web run build
go test ./web
```

UI 改版包含 18 个场景：保留原 7 个回归场景，新增桌面/移动端导航、账户菜单、分页筛选、保存与失败反馈、配置删除后的偏好刷新、登录注册，以及四个宽度下的视觉检查。浏览器测试使用可控 API 替身，不调用真实 AI，也不修改业务数据库。

本机 Ubuntu 20 验收沿用已安装的 Chromium 与 `PLAYWRIGHT_HOST_PLATFORM_OVERRIDE=ubuntu22.04-x64`；其他受支持环境不需要此覆盖值。

开发预览使用 `npm --prefix web run dev`，Vite 将 API 请求代理到本地 8080。使用 Go 内嵌页面时，必须在前端构建后重新编译并启动 API；运行中的旧二进制不会随源文件变化刷新。

登录续期修复另增 9 个浏览器场景（当前共 27 个），覆盖并发续期、表单保留、写请求重放、网络/服务故障、晚到响应、退出失败、跨标签页退出及账户切换隔离。会话配置与升级步骤见项目 README 的“登录会话与自动续期”。


## 订阅创建助手初版布局（2026-09-12，分栏行为已由下文更新）

订阅助手使用右侧面板：页面可用宽度达到 1080px 时与列表并排，宽度取 42% 并限制在 440–560px；更窄时使用全屏对话框。标题和输入区固定，对话与草案独立滚动。切换窗口尺寸保留未发送内容与草案编辑状态，桌面端仍可筛选和编辑订阅列表。

历史对话、API 和模型选择默认收在“助手设置”中，底部显示模型及配置可用状态。“继续调整”聚焦输入框，“编辑草案”展开表单；编辑期间需保存或取消后才能确认创建。Enter 发送，Shift+Enter 换行，中文输入法确认不触发发送。草案发送安排读取账户偏好，匹配预览继续使用助手实际回复。

新增 `tests/subscription-assistant.spec.ts`，覆盖六种宽度、固定输入区、长文本编辑、焦点和尺寸切换、草案版本与过期、显式确认、输入法、缺少 API 和账户加载失败。结合助手、订阅、工作台和 UI 回归共 48 个测试通过；TypeScript 检查、前端构建和 `go test ./web` 通过。

截图使用固定 API 替身数据，预览数量为测试样例：

- [桌面 1920px](test-results/subscription-assistant-assistant-layout-and-editor-at-1920px/assistant-1920.png)
- [桌面 1440px](test-results/subscription-assistant-assistant-layout-and-editor-at-1440px/assistant-1440.png)
- [移动端 390px](test-results/subscription-assistant-assistant-layout-and-editor-at-390px/assistant-390.png)
- [移动端编辑草案](test-results/subscription-assistant-assistant-layout-and-editor-at-390px/assistant-editor-390.png)

复查本次变更可运行：

```bash
npm --prefix web test -- tests/agent.spec.ts tests/subscriptions.spec.ts tests/subscription-assistant.spec.ts tests/ui.spec.ts tests/workspace.spec.ts --workers=4
```

## 论文双面板调整

论文详情中打开助手后，顶部提供左右排列、上下排列、交换位置和恢复默认。左右默认论文 60% / 助手 40%，上下默认均分；实际尺寸遵守最小阅读范围（宽度 320/360px，高度 180/360px）。12px 分隔线支持鼠标和触摸拖动，方向键按 2% 调整，Shift 加速至 10%，Home/End 到达边界，双击均分。交换位置时 DOM 阅读顺序随之变化，尺寸仍跟随面板。

方向、顺序和各方向比例保存在当前浏览器 `signalwatch.paper-layout.v1`，跨论文共享；损坏或不可用的存储不阻断交互。宽度不足 1080px、或上下排列可用高度不足 552px 时自动使用标签页；空间恢复后恢复偏好。窗口尺寸变化本身不写入偏好。

布局调整不重新挂载助手，草稿、选中的模型/会话、运行进度与两侧滚动保留；关闭助手和返回列表保持原有行为。修复了桌面缩至手机时侧栏边距动画造成的短暂横向溢出。新报告用户消息为“快速了解论文”，不替换历史文本。

验收：`scripts/verify.sh` 通过，包括 103 项浏览器测试。新布局测试覆盖键盘、拖动边界、真实触摸事件、取消指针、DOM/草稿/滚动保留、活动任务不重复提交、刷新与响应式恢复、损坏及不可用存储；后端验证文案与内部目标分离、提交幂等和历史/追问不变。截图来自模拟 API，不产生模型费用：

- [左右排列并交换位置](test-results/paper-workspace-panel-keyb-6824a-rve-draft-DOM-and-scrolling/paper-horizontal-swapped.png)
- [上下排列并交换位置](test-results/paper-workspace-panel-keyb-6824a-rve-draft-DOM-and-scrolling/paper-vertical-swapped.png)
- [手机标签页](test-results/paper-workspace-inline-rea-9227c-rsistent-assistant-at-390px/paper-assistant-390.png)

## 订阅共享分栏与 API 用量样式

`ResizableWorkspace` 提供两页共用的 Grid、Pointer Events、键盘和 ResizeObserver 布局，页面只提供标签、内容与偏好配置。论文包装组件继续读写原有 `signalwatch.paper-layout.v1` 及 `paperFirst` 字段；订阅使用独立的 `signalwatch.subscription-layout.v1`（`primaryFirst`）。两页均默认主面板 60% / 助手 40%，上下排列首次均分，最小尺寸、12px 分隔线及快捷键沿用论文交互。

订阅标题和创建按钮位于面板外。助手打开时两侧在视口内独立滚动；关闭后恢复普通列表页面滚动，并把焦点返回「AI 创建订阅」。宽度不足 1080px，或上下排列可用高度不足 552px，显示「订阅列表 / AI 助手」标签页，打开助手默认选中助手。空间恢复后还原已保存布局。小屏助手不再使用全屏对话框，创建、编辑、删除仍为真正模态框，优先处理 Escape；在助手内按 Escape 可关闭助手。

面板使用稳定组件身份和 DOM 顺序，方向、交换及响应式变化保留输入、未保存草案、模型/会话、列表筛选分页、滚动与任务状态；不触发发送或确认创建。列表和编辑弹窗的原有业务行为不变。

用量与调用记录的 `ai-activity-card` 样式限定在该区域：标题 14px/600、正文与表格 13px、说明和筛选标签 12px、控件 14px，沿用系统字体。标题、说明、筛选、分页和加载/错误状态保持桌面 20px、手机 16px 水平内边距。手机筛选纵向排列，表格独立横向滚动；长名称、失败说明和诊断编号换行。统计、UTC、筛选参数与分页逻辑不变。

本次浏览器回归覆盖两页布局、独立偏好、旧论文偏好兼容、草案/列表/任务保留及模态框操作；用量页覆盖 1440px、390px、320px 的字号、边距、四种调用状态、长文本、筛选分页、加载、失败重试和空记录。截图通过模拟 API 生成，不调用真实模型；截图禁用过渡动画以稳定视觉评审。

- [订阅上下排列并交换](test-results/subscription-assistant-sub-76d5b-ith-independent-preferences/subscription-vertical-swapped.png)
- [订阅左右排列并交换](test-results/subscription-assistant-sub-76d5b-ith-independent-preferences/subscription-horizontal-swapped.png)
- [订阅手机标签页](test-results/subscription-assistant-assistant-layout-and-editor-at-390px/assistant-390.png)
- [用量页桌面](test-results/ai-activity-API-activity-typography-and-spacing-at-1440px/api-activity-1440.png)
- [用量页手机](test-results/ai-activity-API-activity-typography-and-spacing-at-390px/api-activity-390.png)
- [用量页 320px](test-results/ai-activity-API-activity-typography-and-spacing-at-320px/api-activity-320.png)

验收结果：隔离 MySQL、Redis 和 Mailpit 环境中的 `scripts/verify.sh` 通过，包含架构检查、Go vet/竞态测试、TypeScript、格式、109 项浏览器测试与前端构建。本次未使用真实模型或业务数据库。
