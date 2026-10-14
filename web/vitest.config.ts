import { defineConfig } from "vitest/config";

// Unit tests of logic that needs no browser; pages are tested end to end (playwright.config.ts).
export default defineConfig({
  test: {
    include: ["{site,console,admin,packages/*}/src/**/*.test.{ts,tsx}"],
  },
});
