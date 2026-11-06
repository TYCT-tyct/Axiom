import { createInstance } from "i18next";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { I18nextProvider } from "react-i18next";
import { describe, expect, it, vi } from "vitest";
import { formatters, moneyInput, parseMoney, useFormat } from "./format";

describe("money", () => {
  // Amounts are billionths of a unit; none may pass through a floating point number.
  it.each([
    { zh: "US$1.50", en: "$1.50", amount: "1500000000" },
    { zh: "US$0.00", en: "$0.00", amount: "0" },
    { zh: "US$0.000000001", en: "$0.000000001", amount: "1" },
    { zh: "US$0.000031", en: "$0.000031", amount: "31000" },
    { zh: "-US$2.25", en: "-$2.25", amount: "-2250000000" },
    { zh: "US$9,223,372,036.854775807", en: "$9,223,372,036.854775807", amount: "9223372036854775807" },
    { zh: "US$10,000,000,000.00", en: "$10,000,000,000.00", amount: "10000000000000000000" },
  ])("formats $amount exactly in both languages", ({ zh, en, amount }) => {
    const money = { amount, currency: "USD" };
    expect(formatters("zh").money(money)).toBe(zh);
    expect(formatters("en").money(money)).toBe(en);
  });
});

describe("decimal", () => {
  // Decimal text from the API is shown grouped for the language, without rounding a digit.
  it.each([
    { text: "1520.5", want: "1,520.5" },
    { text: "0.0000142", want: "0.0000142" },
    { text: "-3", want: "-3" },
    { text: "999999999999999999.999999999999999", want: "999,999,999,999,999,999.999999999999999" },
  ])("formats $text exactly", ({ text, want }) => {
    expect(formatters("zh").decimal(text)).toBe(want);
    expect(formatters("en").decimal(text)).toBe(want);
  });
});

describe("parseMoney", () => {
  it.each([
    { text: "12.5", want: "12500000000" },
    { text: " 0.000000001 ", want: "1" },
    { text: "007", want: "7000000000" },
    { text: "0", want: "0" },
    { text: "9223372036.854775807", want: "9223372036854775807" },
    { text: "10000000000.000000000", want: null },
    { text: "1.0000000001", want: null },
    { text: "-1", want: null },
    { text: "1,5", want: null },
    { text: "1e3", want: null },
    { text: "", want: null },
  ])("reads $text as $want", ({ text, want }) => {
    expect(parseMoney(text)).toBe(want);
  });
});

describe("moneyInput", () => {
  // What a field is filled with must read back as the same amount.
  it.each(["0", "1", "1500000000", "100000000000", "9223372036854775807", "123456789"])("round-trips %s", (amount) => {
    expect(parseMoney(moneyInput(amount))).toBe(amount);
  });
});

describe("pay", () => {
  // A payment amount is in the currency's minor units, so the point moves by that currency's own digits.
  it.each([
    { currency: "USD", amount: "1030", en: "$10.30" },
    { currency: "USD", amount: "1", en: "$0.01" },
    { currency: "JPY", amount: "1000", en: "¥1,000" },
    { currency: "KWD", amount: "1500", en: "KWD\u00a01.500" },
  ])("formats $amount $currency as $en", ({ currency, amount, en }) => {
    expect(formatters("en").pay({ amount, currency })).toBe(en);
  });
  it("formats in the page's language", () => {
    expect(formatters("zh").pay({ amount: "1030", currency: "USD" })).toBe("US$10.30");
  });
});

// Building the Intl formats per component made the admin's 50-row calls list take seconds to render on a starved CI
// runner after its data arrived: a page's rows and times must share one set per language and zone.
it("builds the formats once, however many rows use them", () => {
  const i18n = createInstance();
  i18n.init({ lng: "en", defaultNS: "common", initAsync: false, resources: { en: { common: {} } } });
  const Row = () => useFormat().dateTime(new Date("2026-10-14T12:30:00Z"));
  const rows = Array.from({ length: 50 }, (_, index) => createElement(Row, { key: index }));
  const built = vi.spyOn(Intl, "DateTimeFormat");
  try {
    renderToStaticMarkup(createElement(I18nextProvider, { i18n }, rows));
    expect(built.mock.calls.length).toBeLessThan(rows.length);
  } finally {
    built.mockRestore();
  }
});

// The cache must not keep the viewer in the zone the page opened in: after the system's zone changes, a time shows in
// the new one, even between zones that print the same offset and name today but keep different summer times.
it("follows the viewer's zone when the system's changes", async () => {
  const instant = new Date("2026-10-14T12:30:00Z");
  try {
    vi.stubEnv("TZ", "America/Phoenix");
    expect(formatters("en").dateTime(instant)).toBe("Oct 14, 2026, 05:30:00 MST");
    await Promise.resolve(); // the change arrives in a later task than the render
    vi.stubEnv("TZ", "America/Denver");
    expect(formatters("en").dateTime(instant)).toBe("Oct 14, 2026, 06:30:00 MDT");
  } finally {
    vi.unstubAllEnvs();
  }
});
