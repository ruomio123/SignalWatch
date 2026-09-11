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
- 订阅使用六列表格；宽度不超过 1100px 时改为堆叠信息行，避免平板工作区过窄。论文使用紧凑阅读列表。
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
