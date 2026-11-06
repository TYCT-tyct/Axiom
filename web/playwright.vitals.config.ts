import { defineConfig } from "@playwright/test";
import base from "./playwright.config.ts";
import { freeFirstPort } from "./scripts/free-port.ts";

// The web vitals (e2e/vitals.spec.ts) are timings: they run by themselves, one test at a time, because other tests
// sharing the cores would stretch every load and the 4x CPU throttle is applied on top of whatever CPU the page gets.
//
// When BASE names the commit this tree is measured against (a PR's base or a train's, set by CI), that commit's frontend
// is served beside this one, so the overview's main-thread time is gated relative to it, loaded in turns on the same
// host (docs/frontend.md「实测」). On main and locally BASE is empty and the overview's time is only reported. The chosen
// address reaches the test workers, which load this file again, through AXIOM_VITALS_BASE_PORT, as AXIOM_WEB_PORT does.
const against = process.env.BASE;
const servers = [base.webServer ?? []].flat();
if (against) {
  process.env.AXIOM_VITALS_BASE_PORT ??= String(await apartFrom(Number(process.env.AXIOM_WEB_PORT)));
  const first = Number(process.env.AXIOM_VITALS_BASE_PORT);
  servers.push({
    command: `bash scripts/ci/vitals-base.sh ${against} localhost:${first + 1000} localhost:${first + 1002}`,
    cwd: "..",
    url: `http://localhost:${first + 1000}/console/`,
    reuseExistingServer: false,
    timeout: 600_000, // a runner that never built the base builds it: install, build, embed and compile the server
    gracefulShutdown: { signal: "SIGTERM", timeout: 60_000 }, // the script stops its server and removes its worktree
  });
}

// apartFrom is a free first port whose ports (itself, the next two and the same three 1000 above) miss head's.
async function apartFrom(head: number): Promise<number> {
  for (;;) {
    const first = await freeFirstPort();
    if (![0, 1000, -1000].some((offset) => Math.abs(first + offset - head) <= 2)) {
      return first;
    }
  }
}

export default defineConfig({
  ...base,
  testIgnore: undefined,
  testMatch: "vitals.spec.ts",
  workers: 1,
  webServer: servers,
});
