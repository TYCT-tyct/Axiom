import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { type Language, locales } from "./i18n";

// Money is an exact signed integer in billionths of one unit of a currency, as decimal text, because a JSON number would
// round it in the browser (docs/domains/billing.md「钱的表示」).
export type Money = { amount: string; currency: string };
const moneyDigits = 9;

// decimalOf writes the integer as the decimal it stands for, "1500000000" as "1.500000000", which Intl formats without
// going through a floating point number.
function decimalOf(amount: string) {
  const negative = amount.startsWith("-");
  const digits = (negative ? amount.slice(1) : amount).padStart(moneyDigits + 1, "0");
  return `${negative ? "-" : ""}${digits.slice(0, -moneyDigits)}.${digits.slice(-moneyDigits)}`;
}

// moneyInput writes an amount the way a person would type it back into a field: "1.5", not "1.500000000".
export function moneyInput(amount: string) {
  return decimalOf(amount).replace(/\.?0+$/, "");
}

// parseMoney reads what a person typed, such as "12.5", as an amount in the contract's unit; null when it is not a
// non-negative decimal with at most nine places that fits the contract's 19 digits.
export function parseMoney(text: string): string | null {
  const match = /^(\d+)(?:\.(\d{1,9}))?$/.exec(text.trim());
  if (!match) {
    return null;
  }
  const amount = `${match[1]}${(match[2] ?? "").padEnd(moneyDigits, "0")}`.replace(/^0+(?=\d)/, "");
  return amount.length <= 19 ? amount : null;
}

// Numbers and times are always formatted for the page's language. A time shows in the viewer's time zone unless one is
// given, such as an account's setting, and says which zone it is in, so a time read off a screenshot is never ambiguous.
// The clock has 24 two-digit hours, so times line up in a column, and the zone comes last: Chinese would otherwise put
// it between the date and the time.
export function formatters(language: Language, timeZone?: string) {
  const locale = locales[language];
  const zone = timeZone ? { timeZone } : {};
  const integers = new Intl.NumberFormat(locale);
  const times = new Intl.DateTimeFormat(locale, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
    ...zone,
  });
  const zones = new Intl.DateTimeFormat(locale, { timeZoneName: "short", ...zone });
  const currencies = new Map<string, Intl.NumberFormat>();
  return {
    number: (value: number | bigint) => integers.format(value),
    // An amount of money exactly as the server holds it: never rounded, and at least the currency's two decimals.
    money: ({ amount, currency }: Money) => {
      let format = currencies.get(currency);
      if (!format) {
        format = new Intl.NumberFormat(locale, {
          style: "currency",
          currency,
          minimumFractionDigits: 2,
          maximumFractionDigits: moneyDigits,
        });
        currencies.set(currency, format);
      }
      return format.format(decimalOf(amount) as `${number}`);
    },
    dateTime: (value: Date) =>
      `${times.format(value)} ${zones.formatToParts(value).find((part) => part.type === "timeZoneName")?.value}`,
  };
}

export function useFormat(timeZone?: string) {
  const { i18n } = useTranslation("common");
  const language = i18n.language as Language;
  return useMemo(() => formatters(language, timeZone), [language, timeZone]);
}
