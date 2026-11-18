import type { Schemas } from "@axiom/api";
import { describe, expect, it } from "vitest";
import {
  flattenInvitees,
  passkeyRegistrationAvailability,
  sortSessions,
  validatePasswordChange,
  validateTotpReplace,
} from "./security-data";

describe("session device sorting", () => {
  it("places current device first and sorts remaining devices newest first", () => {
    const sorted = sortSessions([
      { id: "s1", current: false, last_seen_at: "2026-10-20T08:00:00Z" },
      { id: "s2", current: true, last_seen_at: "2026-10-18T08:00:00Z" },
      { id: "s3", current: false, last_seen_at: "2026-10-22T08:00:00Z" },
    ]);
    expect(sorted.map((item) => item.id)).toEqual(["s2", "s3", "s1"]);
  });
});

describe("password and TOTP replacement validation", () => {
  it("validates current password, minimum 12 code points, match, and difference", () => {
    expect(validatePasswordChange("", "123456789012", "123456789012")).toBe("currentMissing");
    expect(validatePasswordChange("oldpass12345", "short", "short")).toBe("tooShort");
    expect(validatePasswordChange("oldpass12345", "newpassword12", "different1234")).toBe("mismatch");
    expect(validatePasswordChange("samepassword1", "samepassword1", "samepassword1")).toBe("same");
    expect(validatePasswordChange("oldpass12345", "brandnewpass1", "brandnewpass1")).toBeNull();
  });

  it("requires proof of current factor and a 6-digit code from the new secret", () => {
    expect(validateTotpReplace("", "123456")).toBe("currentMissing");
    expect(validateTotpReplace("654321", "12345")).toBe("newInvalid");
    expect(validateTotpReplace("aaaa-bbbb-cccc-dddd", "123456")).toBeNull();
  });
});

describe("invitations pagination and 404 fallback", () => {
  it("reports unavailable cleanly when backend node returns 404", () => {
    const flat = flattenInvitees({
      pages: [{ available: false, invitees: [], next_cursor: null }],
      pageParams: [undefined],
    });
    expect(flat.available).toBe(false);
    expect(flat.invitees).toEqual([]);
  });

  it("merges multi-page invitees and propagates API invite link", () => {
    const page1: Schemas["Invitee"][] = [{ registered_at: "2026-10-20T08:00:00Z", available: true }];
    const page2: Schemas["Invitee"][] = [{ registered_at: "2026-10-19T08:00:00Z", available: false }];
    const flat = flattenInvitees({
      pages: [
        { available: true, link: "https://example.com/register?invite=CODE", invitees: page1, next_cursor: "c1" },
        { available: true, invitees: page2, next_cursor: null },
      ],
      pageParams: [undefined, "c1"],
    });
    expect(flat.available).toBe(true);
    expect(flat.link).toBe("https://example.com/register?invite=CODE");
    expect(flat.invitees).toHaveLength(2);
    expect(flat.invitees[1]?.available).toBe(false);
  });
});

describe("passkey registration availability", () => {
  const baseSite: Schemas["Site"] = {
    name: "Axiom",
    currency: "USD",
    key_prefix: "sk-",
    registration: "invite",
    passkeys: true,
    login_domain: "console.example.com",
    login_origin: "https://console.example.com",
  };

  it("requires browser support, site passkeys flag, and matching login origin or domain", () => {
    expect(
      passkeyRegistrationAvailability(baseSite, { origin: "https://console.example.com", hostname: "x" }, false),
    ).toBe("unsupported");
    expect(
      passkeyRegistrationAvailability(
        { ...baseSite, passkeys: false },
        { origin: "https://console.example.com", hostname: "console.example.com" },
        true,
      ),
    ).toBe("site_disabled");
    expect(
      passkeyRegistrationAvailability(
        baseSite,
        { origin: "https://other.example.com", hostname: "other.example.com" },
        true,
      ),
    ).toBe("wrong_domain");
    expect(
      passkeyRegistrationAvailability(
        baseSite,
        { origin: "https://console.example.com", hostname: "console.example.com" },
        true,
      ),
    ).toBe("ready");
    expect(
      passkeyRegistrationAvailability(
        { ...baseSite, login_origin: undefined },
        { origin: "https://console.example.com", hostname: "console.example.com" },
        true,
      ),
    ).toBe("ready");
  });
});
