import { describe, expect, it } from "vitest";
import { formatters } from "./format";
import { initialLanguage } from "./i18n";

describe("initialLanguage", () => {
  it.each([
    { stored: "en", preferred: ["zh-CN"], want: "en" }, // the user's choice wins
    { stored: "fr", preferred: ["zh-TW", "en"], want: "zh" }, // an unknown stored value is ignored
    { stored: null, preferred: ["ja", "en-GB", "zh"], want: "en" }, // the first language offered
    { stored: null, preferred: ["ja"], want: "zh" }, // none offered: Chinese
    { stored: null, preferred: [], want: "zh" },
  ])("$stored, $preferred → $want", ({ stored, preferred, want }) => {
    expect(initialLanguage(stored, preferred)).toBe(want);
  });
});

describe("formatters", () => {
  const instant = new Date("2026-10-14T12:30:00Z");

  it.each([
    { language: "en", zone: "Asia/Shanghai", want: "Oct 14, 2026, 20:30:00 GMT+8" },
    { language: "zh", zone: "UTC", want: "2026年10月14日 12:30:00 UTC" },
    { language: "zh", zone: "America/Los_Angeles", want: "2026年10月14日 05:30:00 GMT-7" },
  ] as const)(
    "shows a time in $zone in $language, with a 24-hour clock and the zone last",
    ({ language, zone, want }) => {
      expect(formatters(language, zone).dateTime(instant)).toBe(want);
    },
  );

  it("groups digits by the language's rules", () => {
    expect(formatters("en").number(1_240_512_880)).toBe("1,240,512,880");
    expect(formatters("zh").number(1_240_512_880n)).toBe("1,240,512,880");
  });

  it.each([
    { language: "en", month: "2026-11", want: "November 2026" },
    { language: "zh", month: "2026-11", want: "2026年11月" },
  ] as const)("reads $month as a calendar month in $language", ({ language, month, want }) => {
    expect(formatters(language).monthLabel(month)).toBe(want);
  });

  it.each([
    { language: "en", month: 11, want: "Nov" },
    { language: "zh", month: 11, want: "11月" },
  ] as const)("names month $month alone in $language", ({ language, month, want }) => {
    expect(formatters(language).monthName(month)).toBe(want);
  });

  it("names the zone its times are in", () => {
    expect(formatters("zh", "UTC").zone()).toBe("UTC");
    expect(formatters("en", "Asia/Shanghai").zone()).toBe("GMT+8");
  });
});
