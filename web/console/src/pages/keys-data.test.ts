import type { Schemas } from "@axiom/api";
import type { TFunction } from "i18next";
import { describe, expect, it } from "vitest";
import { keyRow, spendPoints, today, usedMoney } from "./keys-data";

// What the board's list shows for a key, from what the API says about it. The board itself draws its demo account's
// keys (packages/boards/manifest/keys.ts); these are the values the page puts in that drawing.

type Key = Schemas["Key"];

const now = Date.parse("2026-10-24T10:00:00Z");
const day = (hhmm: string) => Date.parse(`2026-10-24T${hhmm}:00Z`);
const money = (amount: string, currency = "USD") => ({ amount, currency });
// The contract's unit is a billionth of one unit, so ten units are ten billion.
const ten = (units: number) => `${units}000000000`;

const t = ((key: string) => key) as unknown as TFunction;
const format = { money: ({ amount, currency }: { amount: string; currency: string }) => `${currency} ${amount}` };
const context = { format, t, language: "en", now, today: today(now) } as unknown as Parameters<typeof keyRow>[1];

function aKey(over: Partial<Key> = {}): Key {
  return {
    id: "key_1",
    name: "production",
    prefix: "sk-ax-v1-",
    last4: "1234",
    workspace_id: "ws_1",
    created_at: "2026-10-01T00:00:00Z",
    disabled: false,
    ...over,
  };
}

describe("spendPoints", () => {
  it("lifts the highest day to the top of the chart and a day without spending to its floor", () => {
    const points = spendPoints(
      [
        { date: "2026-10-24", amount: money(ten(2)) },
        { date: "2026-10-17", amount: money(ten(1)) },
      ],
      "2026-10-24",
    ).split(" ");
    expect(points).toHaveLength(14);
    // Today is the last point, its own highest day, at the top of the box; a week earlier is half as high; the rest rest
    // on the floor.
    expect(points.at(-1)).toBe("100.0,3.0");
    expect(points[6]).toBe("46.2,10.5");
    expect(points[0]).toBe("0.0,18.0");
  });

  it("draws a flat floor for a key with no spend at all", () => {
    expect(spendPoints(undefined, "2026-10-24")).toBe(
      "0.0,18.0 7.7,18.0 15.4,18.0 23.1,18.0 30.8,18.0 38.5,18.0 46.2,18.0 53.8,18.0 61.5,18.0 69.2,18.0 76.9,18.0 84.6,18.0 92.3,18.0 100.0,18.0",
    );
  });

  it("leaves out a day outside the 14 the API answers with", () => {
    const far = spendPoints([{ date: "2026-09-01", amount: money(ten(9)) }], "2026-10-24");
    const empty = spendPoints(undefined, "2026-10-24");
    expect(far).toBe(empty);
  });
});

describe("usedMoney", () => {
  it("takes what is left from the limit in the contract's unit", () => {
    expect(usedMoney(aKey({ spend_limit: money("500000000000"), limit_remaining: money("68400000000") }))).toEqual({
      amount: "431600000000",
      currency: "USD",
    });
  });

  it("has nothing to say about a key without a limit", () => {
    expect(usedMoney(aKey())).toBeUndefined();
  });
});

describe("keyRow", () => {
  it("shows the name, where the key's text starts and ends, and what it used of its limit", () => {
    const row = keyRow(
      aKey({
        spend_limit: money("500000000000"),
        limit_remaining: money("68400000000"),
        last_used_at: "2026-10-24T09:59:30Z",
      }),
      context,
    );
    expect(row.f5).toBe("production");
    expect(row.f7).toBe("sk-ax-v1-…1234");
    expect(row.f13).toBe("USD 431600000000");
    expect(row.f15).toBe(" / USD 500000000000");
    expect(row.f11).toBe("width: 86%;");
    expect(row.f10).toBe("qpace w");
    expect(row.f67).toBe("keys.board.justNow");
  });

  it("calls a key without a limit unlimited, and one under four fifths of its limit quiet", () => {
    const row = keyRow(aKey({ spend_limit: money(ten(100)), limit_remaining: money(ten(50)) }), context);
    expect(row.f15).toBe(" / USD 100000000000");
    expect(row.f10).toBe("qpace ");
    expect(keyRow(aKey(), context).f15).toBe("keys.noLimit");
  });

  it("counts the models a key may call and whether any address may use it", () => {
    const narrow = keyRow(aKey({ models: ["a", "b", "c"], ip_allow: ["10.0.0.0/24"] }), context);
    expect(narrow.f38).toBe("keys.board.modelCount");
    expect(narrow.f39).toBe("qlim ok");
    expect(narrow.f65).toBe("keys.board.ipRestricted");
    const open = keyRow(aKey(), context);
    expect(open.f38).toBe("keys.board.allModels");
    expect(open.f39).toBe("qlim warn");
    expect(open.f65).toBe("keys.board.anyIp");
  });

  it("says on the line under the name that a key is off or past its expiry", () => {
    expect(keyRow(aKey({ disabled: true }), context).f7).toBe("sk-ax-v1-…1234 · keys.board.disabled");
    expect(keyRow(aKey({ expires_at: "2026-10-01T00:00:00Z" }), context).f7).toBe("sk-ax-v1-…1234 · keys.expired");
    expect(keyRow(aKey({ expires_at: "2027-10-01T00:00:00Z" }), context).f7).toBe("sk-ax-v1-…1234");
  });

  it("says when the key was last used, in the reader's units", () => {
    const when = (at: string) => keyRow(aKey({ last_used_at: at }), { ...context, now: day("12:00") }).f67;
    expect(when("2026-10-24T11:59:40Z")).toBe("keys.board.justNow");
    expect(when("2026-10-24T11:55:00Z")).toBe("5 minutes ago");
    expect(when("2026-10-24T07:00:00Z")).toBe("5 hours ago");
    expect(when("2026-10-23T11:00:00Z")).toBe("yesterday");
    expect(when("2026-09-22T12:00:00Z")).toBe("32 days ago");
    expect(keyRow(aKey(), context).f67).toBe("keys.board.neverUsed");
  });
});
