import { defineConfig, devices } from "@playwright/test";

// The journey (journey/README in web/README.md「测试」) runs one browser against a real Axiom node, which
// journey/node.ts starts with a real PostgreSQL and Valkey; it needs Docker, so it is not part of `just web-e2e`, which
// runs in a container without it.
export default defineConfig({
  testDir: "journey",
  globalSetup: "./journey/node.ts",
  workers: 1, // one story, told in order
  forbidOnly: Boolean(process.env.CI),
  reporter: process.env.CI ? "github" : "list",
  timeout: 90_000,
  use: { locale: "zh-CN", trace: "retain-on-failure" },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
