import { describe, expect, it } from "vitest";
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
