import { stripEmailChangeToken } from "@axiom/app";
import { describe, expect, it } from "vitest";
import { initialGlyph, validateDisplayName, validateEmailChange } from "./settings-data";

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
    expect(stripEmailChangeToken("/console/settings?tab=1#other=1&email-change-token=tok_456")).toEqual({
      token: "tok_456",
      cleanHref: "/console/settings?tab=1#other=1",
    });
    expect(stripEmailChangeToken("/console/settings#email-change-token=tok_123")).toEqual({
      token: "tok_123",
      cleanHref: "/console/settings",
    });
  });
});

describe("profile glyph", () => {
  it("uses display name initial before email", () => {
    expect(initialGlyph("  林知远 ", "alice@example.com")).toEqual({ glyph: "林", title: "林知远" });
    expect(initialGlyph("", "alice@example.com")).toEqual({ glyph: "A", title: "alice@example.com" });
  });
});
