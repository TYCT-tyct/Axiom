import { defineConfig } from "@playwright/test";
import base from "./playwright.config.ts";

export default defineConfig({
  ...base,
  testIgnore: undefined,
  testMatch: "boards*.spec.ts",
  fullyParallel: true,
  workers: 2,
  timeout: 180_000, // Composite boards also advance every embedded demo during the CSP interactions.
  use: {
    ...base.use,
    // The two mounts have different paint histories. Partial raster can leave different SVG edge pixels at the
    // same frozen geometry, so raster whole tiles for both sides of the comparison.
    launchOptions: { args: ["--disable-partial-raster"] },
    // Full DOM snapshots repeatedly serialize the composite Site; explicit artboard/react screenshots remain attached.
    trace: { mode: "retain-on-failure", snapshots: false },
  },
  // These proofs serve their own isolated demo and offline reference; no product build or Go server is needed.
  webServer: undefined,
});
