import { existsSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { join, dirname } from "node:path";

const output = join(dirname(fileURLToPath(import.meta.url)), "../test-results");
const widths = [1440, 768, 390, 320];
const views = [
  ["home", "首页"],
  ["login", "登录"],
  ["register", "注册"],
  ["dashboard", "研究概览"],
  ["subscriptions", "订阅"],
  ["subscription-editor", "订阅编辑"],
  ["papers", "匹配论文"],
  ["paper-detail", "论文详情"],
  ["paper-ai", "AI 论文解读"],
  ["settings", "偏好设置（未配置 AI）"],
  ["settings-configured", "偏好设置（已配置 AI）"],
];
const sections = widths
  .map((width) => {
    const cards = views
      .map(([name, label]) => {
        const path = `ui-visual-review-and-long-content-at-${width}px/${name}-${width}.png`;
        if (!existsSync(join(output, path))) {
          throw new Error(`截图不存在：${path}。请先运行 npm test。`);
        }
        return `<a class="card" href="${path}" target="_blank" rel="noopener"><div class="caption">${label}<span>${width}px ↗</span></div><img src="${path}" alt="${label}，${width} 像素宽度" loading="lazy"></a>`;
      })
      .join("\n");
    return `<section class="grid" data-width="${width}" ${width !== 1440 ? "hidden" : ""}>${cards}</section>`;
  })
  .join("\n");
const html = `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>SignalWatch UI 改版预览</title>
<style>
*{box-sizing:border-box}body{margin:0;background:#f9fafb;color:#111827;font:14px/1.6 system-ui,sans-serif}header{padding:24px;max-width:1440px;margin:auto}h1{font-size:24px;margin:0 0 8px}p{color:#6b7280;margin:0 0 16px}select{font:inherit;padding:8px;border:1px solid #e5e7eb;border-radius:8px;background:white}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(280px,1fr));gap:20px;padding:0 24px 24px;max-width:1440px;margin:auto}.grid[hidden]{display:none}.card{display:block;background:white;border:1px solid #e5e7eb;border-radius:12px;overflow:hidden;text-decoration:none;color:inherit}.caption{display:flex;justify-content:space-between;gap:8px;padding:12px 16px;border-bottom:1px solid #e5e7eb}.caption span{color:#6b7280;font-size:12px}.card img{display:block;width:100%;height:320px;object-fit:contain;object-position:top;background:#f3f4f6}.card:hover{border-color:#818cf8}:focus-visible{outline:2px solid #4f46e5;outline-offset:3px}
</style><header><h1>SignalWatch · UI 改版预览</h1><p>Chromium 实际页面截图，使用固定测试数据。点击图片查看原尺寸；预览不包含真实账户或密钥。</p><label for="width">屏幕宽度 </label><select id="width">${widths.map((w) => `<option value="${w}">${w}px</option>`).join("")}</select></header>${sections}
<script>document.getElementById('width').addEventListener('change',event=>{document.querySelectorAll('[data-width]').forEach(section=>{section.hidden=section.dataset.width!==event.target.value;});});</script></html>`;
writeFileSync(join(output, "ui-review.html"), html);
console.log("截图入口：web/test-results/ui-review.html（44 张）");
