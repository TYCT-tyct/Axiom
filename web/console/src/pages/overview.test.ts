import type { Schemas } from "@axiom/api";
import type { TFunction } from "i18next";
import { describe, expect, it } from "vitest";
import {
  barRows,
  chartLabels,
  dailyRows,
  dailyTotals,
  modelRowsOf,
  monthWindow,
  pickQuota,
  requestRowsOf,
  splitMoney,
  sumTotals,
} from "./overview-data";

type Money = Schemas["Money"];
type Call = Schemas["Call"];
const t = ((key: string) => key) as unknown as TFunction;
const format = {
  money: ({ amount }: Money) => `$${amount}`,
  number: (value: number | bigint) => String(value),
  hour: () => "08:23",
  monthLabel: (month: string) => month,
  dateTime: () => "full time",
};
const money = (amount: string): Money => ({ amount, currency: "USD" });
const aggregate = (bucket: string, calls: string, amount: string, currency = "USD"): Schemas["CallAggregate"] => ({
  bucket,
  calls,
  price: { amount, currency },
});

describe("the month the overview covers", () => {
  it("is the UTC calendar month with the days it has", () => {
    expect(monthWindow(new Date("2026-02-10T23:30:00Z"))).toMatchObject({
      since: "2026-02-01T00:00:00.000Z",
      month: "2026-02",
      monthNumber: 2,
      days: 28,
    });
    expect(monthWindow(new Date("2026-10-24T10:00:00+08:00")).days).toBe(31);
  });
});

describe("the figures", () => {
  it("counts only the account's currency and keeps the exact sums", () => {
    const rows = [
      aggregate("2026-10-01", "2", "1500000000"),
      aggregate("2026-10-02", "1", "2500000000"),
      aggregate("2026-10-02", "9", "999", "EUR"),
    ];
    expect(sumTotals(rows, "USD")).toEqual({ calls: 3n, amount: 4000000000n });
    expect(sumTotals(rows, "EUR")).toEqual({ calls: 9n, amount: 999n });
    expect(sumTotals(undefined, "USD")).toBeUndefined();
  });

  it("buckets a day's calls under the day of the month", () => {
    const daily = dailyTotals([aggregate("2026-10-01", "2", "15"), aggregate("2026-10-02", "1", "25")], "USD");
    expect([...(daily ?? [])]).toEqual([
      [1, 15n],
      [2, 25n],
    ]);
  });

  it("keeps a day and a model whose calls cost nothing", () => {
    const rows = [aggregate("2026-10-01", "5", "0")];
    expect(dailyRows(rows, "USD")).toEqual([{ day: 1, calls: 5n, amount: 0n }]);
    const models = modelRowsOf(rows, "USD", "", format, t);
    expect(models).toHaveLength(1);
    expect(models[0]).toMatchObject({ f3: "2026-10-01", f5: "0%", f7: "$0" });
  });

  it("writes an error's date, reason and full time where a narrow row can still read them", () => {
    const at = new Date(2026, 9, 24, 8, 23);
    const call = {
      id: "call-1",
      at: at.toISOString(),
      model: "deepseek/x",
      key_id: "key-1",
      error_kind: "upstream_timeout",
      price: money("0"),
    } as Call;
    const [row] = requestRowsOf([call], "", new Map(), format, t);
    expect(row).toMatchObject({
      f2: "10/24 08:23",
      f7: "upstream_timeout · deepseek/x",
      f9: "—",
      f10: "",
      f11: "upstream_timeout",
    });
  });

  it("splits the currency and integer digits from the fraction the board sets smaller", () => {
    expect(splitMoney("US$3.141592653")).toEqual(["US$3", ".141592653"]);
    expect(splitMoney("-US$9,223")).toEqual(["-US$9,223", ""]);
  });

  it("draws each day's bar in its slot against the month's largest day", () => {
    const rows = barRows(
      new Map([
        [1, 10n],
        [2, 5n],
      ]),
      31,
    );
    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({ key: "1", f0: "6.5", f3: "190.0" });
    expect(rows[1]).toMatchObject({ key: "2", f0: "38.7", f3: "95.0" });
  });

  it("labels the chart with its dates, or with the card's sentence when it has none", () => {
    expect(chartLabels(31, 10, "")[0]).toEqual({ text: "10/1", left: "left: 1.61%;" });
    expect(chartLabels(31, 10, "No calls this month")[0]?.text).toBe("No calls this month");
    expect(chartLabels(31, 10, "")).toHaveLength(5);
  });
});

describe("the key the notice warns about", () => {
  const key = (over: Partial<Schemas["Key"]>) =>
    ({
      id: "key",
      name: "production",
      disabled: false,
      limit_remaining: money("25000000000"),
      ...over,
    }) as Schemas["Key"];

  it("is the enabled one nearest its limit", () => {
    const quota = pickQuota([
      key({ id: "a", name: "a", spend_limit: money("50000000000"), limit_remaining: money("40000000000") }),
      key({ id: "b", name: "b", spend_limit: money("50000000000"), limit_remaining: money("5000000000") }),
      key({ id: "c", name: "c", disabled: true, spend_limit: money("50000000000"), limit_remaining: money("1") }),
      key({
        id: "d",
        name: "d",
        expires_at: "2020-01-01T00:00:00Z",
        spend_limit: money("50000000000"),
        limit_remaining: money("1"),
      }),
    ]);
    expect(quota).toMatchObject({ percent: 90 });
    expect(quota?.key.id).toBe("b");
  });

  it("is none when no key has a spend limit to run out of", () => {
    expect(pickQuota([key({})])).toBeUndefined();
  });

  it("treats a limit of zero as used up, not as no limit", () => {
    const quota = pickQuota([key({ spend_limit: money("0"), limit_remaining: money("0") })]);
    expect(quota).toMatchObject({ percent: 100 });
  });
});
