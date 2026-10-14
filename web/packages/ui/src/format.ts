import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { type Language, locales } from "./i18n";

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
  return {
    number: (value: number | bigint) => integers.format(value),
    dateTime: (value: Date) =>
      `${times.format(value)} ${zones.formatToParts(value).find((part) => part.type === "timeZoneName")?.value}`,
  };
}

export function useFormat(timeZone?: string) {
  const { i18n } = useTranslation("common");
  const language = i18n.language as Language;
  return useMemo(() => formatters(language, timeZone), [language, timeZone]);
}
