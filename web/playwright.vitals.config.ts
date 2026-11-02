import { defineConfig } from "@playwright/test";
import base from "./playwright.config.ts";

// The web vitals (e2e/vitals.spec.ts) are timings: they run by themselves, one test at a time, because other tests
// sharing the cores would stretch every load and the 4x CPU throttle is applied on top of whatever CPU the page gets.
export default defineConfig({ ...base, testIgnore: undefined, testMatch: "vitals.spec.ts", workers: 1 });
