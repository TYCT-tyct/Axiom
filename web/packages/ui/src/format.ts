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

// minorOf writes an amount in minor units as the decimal it means: "1030" in USD as "10.30", "1000" in JPY as "1000".
// It shifts digits in text, never through a floating point number.
function minorOf(amount: string, digits: number) {
  if (digits === 0) {
    return amount;
  }
  const negative = amount.startsWith("-");
  const value = (negative ? amount.slice(1) : amount).padStart(digits + 1, "0");
  return `${negative ? "-" : ""}${value.slice(0, -digits)}.${value.slice(-digits)}`;
}

// parseMoney reads what a person typed, such as "12.5", as an amount in the contract's unit; null when it is not a
// non-negative decimal with at most nine places that fits maxDigits, by default the contract's 19.
export function parseMoney(text: string, maxDigits = 19): string | null {
  const match = /^(\d+)(?:\.(\d{1,9}))?$/.exec(text.trim());
  if (!match) {
    return null;
  }
  const amount = `${match[1]}${(match[2] ?? "").padEnd(moneyDigits, "0")}`.replace(/^0+(?=\d)/, "");
  return amount.length <= maxDigits ? amount : null;
}

// A calendar month is a month, not an instant: both formats read it as UTC so the viewer's zone cannot move it to the
// previous or next day. The picker needs the month alone; a title needs the month and year.
function monthFormats(locale: string) {
  const months = new Intl.DateTimeFormat(locale, { month: "short", timeZone: "UTC" });
  const labels = new Intl.DateTimeFormat(locale, { year: "numeric", month: "long", timeZone: "UTC" });
  return {
    monthName: (month: number) => months.format(new Date(Date.UTC(2000, month - 1, 1))),
    monthLabel: (month: string) => {
      const year = Number(month.slice(0, 4));
      const number = Number(month.slice(5, 7));
      return labels.format(new Date(Date.UTC(year, number - 1, 1)));
    },
  };
}

// Numbers and times are always formatted for the page's language. A time shows in the viewer's time zone unless one is
// given, such as an account's setting, and says which zone it is in, so a time read off a screenshot is never ambiguous.
// The clock has 24 two-digit hours, so times line up in a column, and the zone comes last: Chinese would otherwise put
// it between the date and the time.
//
// Building the Intl formats costs over a millisecond on a fast core, and a page calls useFormat in every row and every
// time it shows: rebuilt per component, the 50 rows of the admin's calls list took 0.7 s to render after their data
// arrived, and over 5 s on a starved CI runner. So each language and zone builds them once for the page's lifetime.
const built = new Map<string, ReturnType<typeof build>>();

export function formatters(language: Language, timeZone?: string) {
  const zone = timeZone ?? viewerZone();
  const key = `${language} ${zone}`;
  let format = built.get(key);
  if (!format) {
    format = build(language, zone);
    built.set(key, format);
  }
  return format;
}

// The viewer's zone can change while the page is open, when the system's does. Resolving it builds an Intl format,
// half the cost the cache above saves if every row did it, so one task resolves it once: a render of a whole list runs
// in one task, and the next task reads the zone afresh.
let viewer: string | undefined;

function viewerZone() {
  if (viewer === undefined) {
    viewer = Intl.DateTimeFormat().resolvedOptions().timeZone;
    queueMicrotask(() => {
      viewer = undefined;
    });
  }
  return viewer;
}

function build(language: Language, timeZone: string) {
  const locale = locales[language];
  const zone = { timeZone };
  const integers = new Intl.NumberFormat(locale);
  // An exact decimal in text, such as a ratio or a supplier's own balance: grouped for the language, every digit kept.
  const decimals = new Intl.NumberFormat(locale, { maximumFractionDigits: 100 });
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
  const months = monthFormats(locale);
  const currencies = new Map<string, Intl.NumberFormat>();
  // A payment's minor unit is the currency's, not always two digits (JPY, KWD), and is resolved once per currency.
  const payDigits = new Map<string, number>();
  const payFormats = new Map<string, Intl.NumberFormat>();
  return {
    number: (value: number | bigint) => integers.format(value),
    decimal: (text: string) => decimals.format(text as `${number}`),
    // A price in the currency a payment method is paid in, in that currency's minor units (cents, fen, or none): the
    // payment contract's PayAmount, not the site's billionths. The fraction digits are the currency's own.
    pay: ({ amount, currency }: Money) => {
      let digits = payDigits.get(currency);
      if (digits === undefined) {
        digits =
          new Intl.NumberFormat(locale, { style: "currency", currency }).resolvedOptions().maximumFractionDigits ?? 2;
        payDigits.set(currency, digits);
      }
      let format = payFormats.get(currency);
      if (!format) {
        format = new Intl.NumberFormat(locale, {
          style: "currency",
          currency,
          minimumFractionDigits: digits,
          maximumFractionDigits: digits,
        });
        payFormats.set(currency, format);
      }
      return format.format(minorOf(amount, digits) as `${number}`);
    },
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
    // The short name of the zone dateTime uses, for a sentence that says which zone the page's times are in.
    zone: () => zones.formatToParts(new Date()).find((part) => part.type === "timeZoneName")?.value ?? zone.timeZone,
    // The month's name alone for a picker's options, and "YYYY-MM" as a month and year for a title.
    monthName: months.monthName,
    monthLabel: months.monthLabel,
  };
}

export function useFormat(timeZone?: string) {
  const { i18n } = useTranslation("common");
  const language = i18n.language as Language;
  return formatters(language, timeZone);
}
