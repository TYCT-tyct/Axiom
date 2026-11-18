import type { Schemas } from "@axiom/api";
import type { TFunction } from "i18next";
import { describe, expect, it } from "vitest";
import {
  buildInvitationRows,
  buildSessionRows,
  flattenInvitees,
  sortSessions,
  validatePasswordChange,
  validateTotpReplace,
} from "./security-data";

const t = ((key: string) => key) as unknown as TFunction;

describe("session device sorting and protection", () => {
  it("places current device first and sorts remaining devices newest first", () => {
    const sorted = sortSessions([
      { id: "s1", current: false, last_seen_at: "2026-10-20T08:00:00Z" },
      { id: "s2", current: true, last_seen_at: "2026-10-18T08:00:00Z" },
      { id: "s3", current: false, last_seen_at: "2026-10-22T08:00:00Z" },
    ]);
    expect(sorted.map((item) => item.id)).toEqual(["s2", "s3", "s1"]);
  });

  it("renders a tag without revoke button for current device", () => {
    const rows = buildSessionRows(t, () => "formatted", {
      available: true,
      sessions: [
        { id: "s2", current: true, client: "Chrome on Linux", ip_group: "203.0.113.1" },
        { id: "s3", current: false, client: "Firefox on macOS", ip_group: "203.0.113.2" },
      ],
    });
    expect(rows[0]).toMatchObject({ kind: "0", f4: "security.sessions.currentTag" });
    expect(rows[1]).toMatchObject({ kind: "1", f5: "security.sessions.revoke" });
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
    expect(buildInvitationRows(t, () => "time", flat)[0]).toMatchObject({
      key: "unavailable",
      f1: "security.invitations.unavailable",
    });
  });

  it("merges multi-page invitees and localizes disabled invitee status", () => {
    const page1: Schemas["Invitee"][] = [{ registered_at: "2026-10-20T08:00:00Z", available: true }];
    const page2: Schemas["Invitee"][] = [{ registered_at: "2026-10-19T08:00:00Z", available: false }];
    const flat = flattenInvitees({
      pages: [
        { available: true, invitees: page1, next_cursor: "c1" },
        { available: true, invitees: page2, next_cursor: null },
      ],
      pageParams: [undefined, "c1"],
    });
    const rows = buildInvitationRows(t, () => "time", flat);
    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({ f4: "security.invitations.statusOk" });
    expect(rows[1]).toMatchObject({ f4: "security.invitations.statusUnavailable" });
  });
});
