import { defineConfig, devices } from "@playwright/test";
import { apps } from "./e2e/apps.ts";

// End-to-end tests run against the production builds served by `vite preview` under the pages' content security
// policy (vite.shared.ts), so a page that breaks the policy fails here. `just web` builds first.
export default defineConfig({
  testDir: "e2e",
  forbidOnly: Boolean(process.env.CI),
  reporter: process.env.CI ? "github" : "list",
  use: {
    baseURL: "http://localhost:6174",
    locale: "zh-CN",
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  // Never reuse a server already listening: it may be another worktree's build, and this one's tests would pass or fail
  // on it. A busy port fails the run instead (the preview servers use strictPort).
  webServer: apps.map((app) => ({
    command: `pnpm --filter ${app.filter} preview`,
    url: app.url,
    reuseExistingServer: false,
  })),
});
