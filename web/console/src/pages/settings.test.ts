import type { TFunction } from "i18next";
import { describe, expect, it } from "vitest";
import {
  buildBillingRows,
  initialGlyph,
  readEmailChangeToken,
  stripEmailChangeHash,
  validateDisplayName,
  validateEmailChange,
} from "./settings-data";

const t = ((key: string, opts?: Record<string, string>) =>
  opts ? `${key}:${JSON.stringify(opts)}` : key) as unknown as TFunction;

describe("display name validation", () => {
  it("counts Unicode code points after trimming whitespace", () => {
    expect(validateDisplayName("   ")).toBe("empty");
    expect(validateDisplayName("  林知远  ")).toBeNull();
    expect(validateDisplayName(` ${"🎉".repeat(100)} `)).toBeNull();
    expect(validateDisplayName("🎉".repeat(101))).toBe("tooLong");
  });
});

describe("email change validation and hash token helpers", () => {
  it("validates format, difference from current email, and password presence", () => {
    expect(validateEmailChange("not-an-email", "user@example.com", "pw")).toBe("invalid");
    expect(validateEmailChange("USER@example.com", "user@example.com", "pw")).toBe("unchanged");
    expect(validateEmailChange("new@example.com", "user@example.com", "")).toBe("passwordRequired");
    expect(validateEmailChange("new@example.com", "user@example.com", "secret")).toBeNull();
  });

  it("extracts and removes email-change-token without dropping pathname or query", () => {
    expect(readEmailChangeToken("#email-change-token=tok_123")).toBe("tok_123");
    expect(readEmailChangeToken("#other=1&email-change-token=tok_456")).toBe("tok_456");
    expect(readEmailChangeToken("#other=1")).toBeNull();
    expect(
      stripEmailChangeHash({
        pathname: "/console/settings",
        search: "?tab=1",
        hash: "#other=1&email-change-token=tok_456",
      }),
    ).toBe("/console/settings?tab=1#other=1");
    expect(
      stripEmailChangeHash({
        pathname: "/console/settings",
        search: "",
        hash: "#email-change-token=tok_123",
      }),
    ).toBe("/console/settings");
  });
});

describe("profile glyph and billing rows", () => {
  it("uses display name initial before email", () => {
    expect(initialGlyph("  林知远 ", "alice@example.com")).toEqual({ glyph: "林", title: "林知远" });
    expect(initialGlyph("", "alice@example.com")).toEqual({ glyph: "A", title: "alice@example.com" });
  });

  it("marks active billing choice with a tag and inactive options with buttons", () => {
    const rows = buildBillingRows(t, {
      source: "account",
      preference: "wallet_first",
      site_default: "subscription_first",
    });
    expect(rows.find((row) => row.key === "wallet_first")).toMatchObject({ kind: "0", f3: "qtag ok" });
    expect(rows.find((row) => row.key === "site")).toMatchObject({ kind: "1" });
    expect(rows.find((row) => row.key === "subscription_only")).toMatchObject({ kind: "1" });
  });
});
