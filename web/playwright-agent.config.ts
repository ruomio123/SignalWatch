import { defineConfig } from "@playwright/test";
import { fileURLToPath } from "node:url";

const artifacts = fileURLToPath(
  new URL("../.cache/test-artifacts/playwright-agent/", import.meta.url),
);

export default defineConfig({
  testDir: "./tests",
  testMatch: "agent.spec.ts",
  outputDir: `${artifacts}results`,
  forbidOnly: true,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 20_000,
  expect: { timeout: 7_000 },
  reporter: [
    ["list"],
    ["html", { outputFolder: `${artifacts}report`, open: "never" }],
  ],
  use: {
    baseURL: "http://127.0.0.1:4173",
    browserName: "chromium",
    headless: true,
    serviceWorkers: "block",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  webServer: {
    command:
      "node node_modules/vite/bin/vite.js --config vite.agent-test.config.ts --host 127.0.0.1 --port 4173 --strictPort",
    url: "http://127.0.0.1:4173",
    reuseExistingServer: false,
    timeout: 20_000,
  },
});
