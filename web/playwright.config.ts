import { createServer } from "node:net";
import { defineConfig, devices } from "@playwright/test";

// Whether nothing listens on the port, on the addresses a preview server may take.
function isFree(port: number): Promise<boolean> {
  const tryListen = (host: string) =>
    new Promise<boolean>((resolve) => {
      const server = createServer();
      // An address the machine does not have cannot be taken by anyone else.
      server.once("error", (error: NodeJS.ErrnoException) => resolve(error.code !== "EADDRINUSE"));
      server.listen(port, host, () => server.close(() => resolve(true)));
    });
  return Promise.all([tryListen("127.0.0.1"), tryListen("::1")]).then((answers) => answers.every(Boolean));
}

// A first port from which the three apps' dev ports and their preview ports (vite.shared.ts) are all free. It is random,
// so two runs starting together do not look at the same ports one after the other and both take them.
async function freeFirstPort(): Promise<number> {
  for (let attempt = 0; attempt < 100; attempt++) {
    const first = 20_000 + Math.floor(Math.random() * 20_000);
    const wanted = [0, 1, 2].flatMap((app) => [first + app, first + app + 1000]);
    if ((await Promise.all(wanted.map(isFree))).every(Boolean)) {
      return first;
    }
  }
  throw new Error("no free ports for the preview servers");
}

// Unless AXIOM_WEB_PORT names the ports, this run takes free ones, so another worktree's checks can run at the same time.
// The variable also carries the choice to the test workers, which load this file again, and to the preview servers.
process.env.AXIOM_WEB_PORT ??= String(await freeFirstPort());
const { apps } = await import("./e2e/apps.ts");

// End-to-end tests run against the production builds served by `vite preview` under the pages' content security
// policy (vite.shared.ts), so a page that breaks the policy fails here. `just web` builds first, stamped with a version.
export default defineConfig({
  testDir: "e2e",
  forbidOnly: Boolean(process.env.CI),
  reporter: process.env.CI ? "github" : "list",
  use: {
    baseURL: apps[1].url,
    locale: "zh-CN",
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  // Never reuse a server already listening: it may be another worktree's build, and this one's tests would pass or fail
  // on it. The ports are free when chosen; one taken since fails the run (the preview servers use strictPort).
  webServer: apps.map((app) => ({
    command: `pnpm --filter ${app.filter} preview`,
    url: app.url,
    reuseExistingServer: false,
  })),
});
