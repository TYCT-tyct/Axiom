import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { useEffect, useState } from "react";

export type CreditBatch = Schemas["CreditBatch"];
export type BalanceData = Schemas["Balance"];
export type StatementRow = Schemas["StatementRow"];
export type StatementDay = Schemas["StatementDay"];
export type AccountSubscription = Schemas["AccountSubscription"];
export type PaymentMethodQuote = Schemas["PaymentMethod"];
export type Money = Schemas["Money"];
export type BoardKind = "desktop" | "phone";

const BILLION = 1_000_000_000n;
const HIGH_DENOM_CURRENCIES = new Set(["CNY", "JPY", "KRW", "INR", "TWD", "HKD"]);
const CURRENCY_PREFIXES: Record<string, string> = {
  USD: "$",
  CNY: "¥",
  JPY: "¥",
  EUR: "€",
  GBP: "£",
  HKD: "HK$",
  TWD: "NT$",
  KRW: "₩",
  INR: "₹",
};

function parseBillionths(amount: string): bigint {
  return BigInt(amount.trim());
}

/** Computes net credit change across a day's summary buckets in billionth units. */
export function dailyNetChange(day: StatementDay): string {
  const positive = parseBillionths(day.topped_up.amount) + parseBillionths(day.gifted.amount);
  const negative = parseBillionths(day.spent.amount) + parseBillionths(day.expired.amount);
  return (positive - negative).toString();
}

/** Returns exact total frozen credits across credit batches, or null when batch breakdown is unavailable. */
export function totalFrozenFromBatches(batches: readonly CreditBatch[] | undefined): string | null {
  if (!batches) return null;
  let total = 0n;
  for (const batch of batches) total += parseBillionths(batch.frozen.amount);
  return total.toString();
}

/** Computes post-top-up balance as exact billionths when balance and positive credits are both available. */
export function projectedBalanceAfter(current: Money | undefined | null, topUpCredits: string | null): Money | null {
  if (!current || !topUpCredits) return null;
  const delta = parseBillionths(topUpCredits);
  if (delta <= 0n) return null;
  return { amount: (parseBillionths(current.amount) + delta).toString(), currency: current.currency };
}

/** Splits formatted currency output into prefix, whole digits, and fractional suffix for board typography. */
export function splitMoneyParts(formatted: string): { currency: string; whole: string; cents: string } {
  const match = formatted.match(/^([^\d]*)(\d{1,3}(?:[,\s\u00a0\u202f']\d{3})+|\d+)(.*)$/u);
  if (!match) return { currency: "", whole: formatted, cents: "" };
  return { currency: match[1] ?? "", whole: match[2] ?? formatted, cents: match[3] ?? "" };
}

/** Returns display prefix symbol for the account currency when showing bare decimal input. */
export function currencyInputPrefix(currency: string): string {
  return CURRENCY_PREFIXES[currency.toUpperCase()] ?? currency;
}

/** Chooses whole-unit quick top-up presets appropriate for the account currency scale. */
export function quickAmountsForCurrency(currency: string): readonly string[] {
  return HIGH_DENOM_CURRENCIES.has(currency.toUpperCase()) ? ["100", "200", "500", "1000"] : ["10", "25", "50", "100"];
}

/** Formats a quick preset amount with integer billionth math via the caller's formatter, in whole units as the board
 * writes the presets (¥100, not ¥100.00): a preset is a whole number of units, so its fraction is always zero. */
export function formatQuickAmount(units: string, currency: string, formatMoney: (value: Money) => string): string {
  const parts = splitMoneyParts(formatMoney({ amount: (BigInt(units.trim()) * BILLION).toString(), currency }));
  return `${parts.currency}${parts.whole}${parts.cents.replace(/^[.,]0+/u, "")}`;
}

/** Detects local IANA time zone with UTC fallback for calendar-day statement queries. */
export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

export const statementDailyQuery = (timeZone: string) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "statementDaily", timeZone],
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        consoleApi.GET("/api/console/v1/statement/daily", {
          params: { query: { cursor: pageParam, limit: 50, time_zone: timeZone } },
          signal,
        }),
      ),
    getNextPageParam: (lastPage) => lastPage.next_cursor ?? undefined,
  });

export const accountSubscriptionsQuery = queryOptions({
  queryKey: [accountKey, "accountSubscriptions"],
  queryFn: ({ signal }) => unwrap(consoleApi.GET("/api/console/v1/subscriptions", { signal })),
});

export function useBoardKind(): BoardKind {
  const [desktop, setDesktop] = useState(() =>
    typeof window !== "undefined" && typeof window.matchMedia === "function"
      ? window.matchMedia("(min-width: 820px)").matches
      : true,
  );
  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return;
    const media = window.matchMedia("(min-width: 820px)");
    const update = () => setDesktop(media.matches);
    update();
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);
  return desktop ? "desktop" : "phone";
}
