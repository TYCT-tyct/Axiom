import { describe, expect, it } from "vitest";
import adminConfig from "../admin/vite.config.ts";
import consoleConfig from "../console/vite.config.ts";
import siteConfig from "../site/vite.config.ts";
import { firstAppPort, servedOffset } from "../vite.shared.ts";

// AXIOM_WEB_PORT moves the three apps' dev server ports together, and their test servers sit servedOffset above them.
describe("firstAppPort", () => {
  it("defaults to 5173", () => {
    expect(firstAppPort(undefined)).toBe(5173);
  });

  it("takes the first of the three ports", () => {
    expect(firstAppPort("5273")).toBe(5273);
  });

  it("takes the last value whose three test servers are still ports", () => {
    const last = 65_535 - 2 - servedOffset;
    expect(firstAppPort(String(last))).toBe(last);
    expect(() => firstAppPort(String(last + 1))).toThrow(/leave room/);
  });

  it.each(["", "0", "-1", "5173.5", "abc", "1e3", "0x10", " 5173", "+5173", "65534"])("refuses %j", (value) => {
    expect(() => firstAppPort(value)).toThrow(/AXIOM_WEB_PORT must leave room for three ports/);
  });
});

// The dev servers forward /api to the program from `just up`: the admin app's API is on the admin listener, the others' on
// the public one.
describe("the API each dev server forwards to", () => {
  it.each([
    ["site", siteConfig, "http://127.0.0.1:8080"],
    ["console", consoleConfig, "http://127.0.0.1:8080"],
    ["admin", adminConfig, "http://127.0.0.1:8082"],
  ])("%s", (_, config, target) => {
    expect(config.server?.proxy?.["/api"]).toBe(target);
  });
});
