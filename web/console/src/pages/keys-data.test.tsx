// @vitest-environment jsdom

import type { Schemas } from "@axiom/api";
import Keys from "@axiom/boards/AxApp/regions/Keys";
import type { TFunction } from "i18next";
import { describe, expect, it } from "vitest";
import { render, setUpPages } from "../testing";
import { keyRow, spendPoints, today, usedMoney } from "./keys-data";

// What the board's list shows for a key, from what the API says about it. The board itself draws its demo account's
// keys (packages/boards/manifest/keys.ts); these are the values the page puts in that drawing.

type Key = Schemas["Key"];

const now = Date.parse("2026-10-24T10:00:00Z");
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

describe("the board's row", () => {
  setUpPages();

  // What a reader sees, not what a field number holds: the region owns which field goes where, so a row is checked
  // through the component the page renders.
  async function row(key: Key) {
    const item = keyRow(key, {
      ...context,
      format: { money: ({ amount }: { amount: string }) => `$${amount}` } as unknown as typeof context.format,
    });
    const page = await render(
      <Keys
        t={(name) => name}
        text={{ usedHeader: "used" }}
        rows={{ keys0: [item] }}
        values={{ decorative: true }}
        actions={null}
      />,
    );
    return page.container;
  }

  it("shows a key's name, its text, what it used of its limit and when it was last used", async () => {
    const table = await row(
      aKey({
        name: "Laptop",
        spend_limit: { amount: "20000000000", currency: "USD" },
        limit_remaining: { amount: "15000000000", currency: "USD" },
        limit_reset: "monthly",
        last_used_at: "2026-10-24T09:55:00Z",
      }),
    );
    // Each value in the cell the board draws it in: a field in the wrong place shows the key's name where its text
    // starts and ends, and every other value one place along.
    expect(table.querySelector(".qkn b")?.textContent).toBe("Laptop");
    expect(table.querySelector(".qkn span")?.textContent).toBe("sk-ax-v1-\u20261234");
    expect(table.querySelector("td:nth-child(2) .qpace")?.className).toContain("qpace");
    expect(table.querySelector("td:nth-child(2) .qpace i")?.getAttribute("style")).toBe("width: 25%;");
    expect(table.querySelector("td:nth-child(2) .qkm b")?.textContent).toBe("$5000000000");
    expect(table.querySelector("td:nth-child(2) .qkm .mut")?.textContent).toBe(
      " / $20000000000 \u00b7 keys.board.period.monthly",
    );
    expect(table.querySelector("polyline")?.getAttribute("points")).toBe(spendPoints(undefined, context.today));
    expect(table.querySelector("td:nth-child(4) .qlim")?.textContent).toBe("keys.board.allModels");
    expect(table.querySelector("td:nth-child(4) .qlim:nth-child(2)")?.textContent).toBe("keys.board.anyIp");
    expect(table.querySelector("td:nth-child(5)")?.textContent).toBe("5 minutes ago");
    // The header says what the figure is: the limit's own period, not the month.
    expect(table.querySelector("thead th:nth-child(2)")?.textContent).toBe("used");
  });

  it("counts the models a key may call and marks the addresses it may be used from", async () => {
    const text = (await row(aKey({ models: ["a", "b"], ip_allow: ["10.0.0.0/24"] }))).textContent ?? "";
    expect(text).toContain("keys.board.modelCount");
    expect(text).toContain("keys.board.ipRestricted");
  });

  it("names a key that is off, and one that is past its expiry before that", async () => {
    expect((await row(aKey({ disabled: true }))).textContent).toContain("keys.board.disabled");
    const expired = await row(aKey({ disabled: true, expires_at: "2026-10-01T00:00:00Z" }));
    expect(expired.textContent).toContain("keys.expired");
    expect(expired.textContent).not.toContain("keys.board.disabled");
  });
});
