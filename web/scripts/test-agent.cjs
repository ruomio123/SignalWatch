require("./check-node.cjs");
const { mkdirSync } = require("node:fs");
const { resolve } = require("node:path");
const { spawnSync } = require("node:child_process");

const webRoot = resolve(__dirname, "..");
const cache = resolve(webRoot, "../.cache");
// Keep only OS/runtime settings. Tests never inherit application credentials.
const env = {};
for (const key of [
  "PATH",
  "HOME",
  "LANG",
  "LC_ALL",
  "CI",
  "TERM",
  "DISPLAY",
  "XDG_RUNTIME_DIR",
])
  if (process.env[key] !== undefined) env[key] = process.env[key];
for (const [key, folder] of Object.entries({
  TMPDIR: "tmp",
  TMP: "tmp",
  TEMP: "tmp",
  npm_config_cache: "npm",
  NODE_COMPILE_CACHE: "node-compile",
  PLAYWRIGHT_BROWSERS_PATH: "playwright-browsers",
})) {
  env[key] = resolve(cache, folder);
  mkdirSync(env[key], { recursive: true });
}
const child = spawnSync(
  process.execPath,
  [
    require.resolve("@playwright/test/cli"),
    "test",
    "--config",
    "playwright-agent.config.ts",
    ...process.argv.slice(2),
  ],
  { cwd: webRoot, env, stdio: "inherit" },
);
if (child.error) console.error(child.error.message);
process.exit(child.status ?? 1);
