import { defineConfig } from "@playwright/test";
import base from "./playwright.config.ts";

// The web vitals (e2e/vitals.spec.ts) are timings: they run by themselves, one test at a time, because other tests
// sharing the cores would stretch every load and the 4x CPU throttle is applied on top of whatever CPU the page gets.
// The docs call the overview over its raw ceiling a busy-host problem, not a page regression (docs/frontend.md「实测」),
// so retry once: a host spike then costs a second run, while a page that really got slower stays over on both.
export default defineConfig({ ...base, testIgnore: undefined, testMatch: "vitals.spec.ts", workers: 1, retries: 1 });
