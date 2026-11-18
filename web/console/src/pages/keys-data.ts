import type { Schemas } from "@axiom/api";
import type { Props } from "@axiom/boards/AxApp/regions/Keys";
import type { useFormat } from "@axiom/ui";
import type { TFunction } from "i18next";
import { maskedKey } from "../parts";

// The rows of the board's key list (#747 C1), filled from the API. The board draws its own demo account's keys
// (Keys.demo.ts); this file is what replaces them, so every value a row shows is one the list gave.

type Key = Schemas["Key"];
type Row = Props["rows"]["keys0"][number];
type Format = ReturnType<typeof useFormat>;
type Spend = NonNullable<Key["daily_spend"]>;
type Money = Schemas["Money"];

// The parts of a row that are the board's drawing rather than its data: the icons' paths and the classes of the cells
// they sit in. Only the text and the classes the key's own figures pick change from one row to the next.
const drawn = {
  f0: "button",
  f1: "0",
  f2: "",
  f3: "qkn",
  f4: "mono",
  f6: "mono mut",
  f8: "qpc",
  f9: "qkm",
  f12: "num",
  f14: "mut",
  f16: "phx",
  f17: "qksp",
  f18: "0 0 100 20",
  f19: "none",
  f20: "true",
  f22: "phx",
  f23: "qlims",
  f24: "qlim",
  f25: "ic ic-models ",
  f26: "0 0 20 20",
  f27: "true",
  f28: "o",
  f29: "a1",
  f30: "M10 2.75 16.5 6.4v7.2L10 17.25 3.5 13.6V6.4z",
  f31: "a2",
  f32: "M3.75 6.5 10 10l6.25-3.5M10 10v7",
  f33: "f",
  f34: "a1",
  f35: "M10 2.75 16.5 6.4v7.2L10 17.25 3.5 13.6V6.4z",
  f36: "a2",
  f37: "M3.75 6.5 10 10l6.25-3.5M10 10v7",
  f40: "ic ic-security ",
  f41: "0 0 20 20",
  f42: "true",
  f43: "o",
  f44: "a1",
  f45: "4",
  f46: "9",
  f47: "12",
  f48: "8.5",
  f49: "2.25",
  f50: "a2 nf",
  f51: "M6.75 9V6.75a3.25 3.25 0 0 1 6.5 0V9",
  f52: "a3 cut",
  f53: "M10 12.25v2",
  f54: "f",
  f55: "a1",
  f56: "4",
  f57: "9",
  f58: "12",
  f59: "8.5",
  f60: "2.25",
  f61: "a2 nf",
  f62: "M6.75 9V6.75a3.25 3.25 0 0 1 6.5 0V9",
  f63: "a3 cut",
  f64: "M10 12.25v2",
  f66: "r phx",
  f68: "r",
  f69: "ic ic-chev qra",
  f70: "0 0 20 20",
  f71: "true",
  f72: "o",
  f73: "a1 nf",
  f74: "m6 8 4 4 4-4",
  f75: "f",
  f76: "a1 nf",
  f77: "m6 8 4 4 4-4",
} as const;

// The chart's box in its viewBox: the last 14 days, the highest day at the top of the box and a day without spending on
// its floor.
const chartDays = 14;
const chartWidth = 100;
const chartTop = 3;
const chartBottom = 18;

// Where the used part of a key's limit stands, as the board's bar shows it: its share of the limit, and the warning the
// board's own demo key at 86% carries from 80% on.
const barWarn = 0.8;

const days = 86400000;

// spendPoints draws the last 14 days of a key's spend, oldest first. A day the answer left out is a day without
// spending, and a key nothing was spent on is a flat line on the floor. Each key is drawn against its own highest day:
// the sparkline says the shape of the spend, and the amounts are in the cell beside it.
export function spendPoints(spend: Spend | undefined, today: string): string {
  const values = Array<number>(chartDays).fill(0);
  for (const { date, amount } of spend ?? []) {
    const index = daysBetween(date, today);
    if (index >= 0 && index < chartDays) {
      values[chartDays - 1 - index] = Number(amount.amount);
    }
  }
  const highest = Math.max(...values);
  return values
    .map((value, index) => {
      const x = (index * chartWidth) / (chartDays - 1);
      const y = chartBottom - (highest > 0 ? (chartBottom - chartTop) * (value / highest) : 0);
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
}

// daysBetween is how many whole days after date today is, both read as UTC days, which is how the spend's days are cut.
function daysBetween(date: string, today: string) {
  return Math.round((Date.parse(today) - Date.parse(date)) / days);
}

// usedMoney is what a key spent of its limit in the limit's current window: the balance says what is left of a limit,
// never what was used, so the page takes one from the other in the contract's integer unit.
export function usedMoney(key: Key): Money | undefined {
  const limit = key.spend_limit;
  if (!limit) {
    return undefined;
  }
  const used = BigInt(limit.amount) - BigInt(key.limit_remaining?.amount ?? limit.amount);
  return { amount: used.toString(), currency: limit.currency };
}

// limitShare is how much of the limit is gone, for the bar; a limit of zero is drawn as an empty bar rather than as a
// division by it.
function limitShare(used: Money | undefined, limit: Money | undefined) {
  if (!used || !limit || BigInt(limit.amount) <= 0n) {
    return 0;
  }
  return Number((BigInt(used.amount) * 10000n) / BigInt(limit.amount)) / 10000;
}

// relativeTime says when a key was last used the way the board's column does: "刚刚", then minutes, hours, a day, days.
// The reader's language decides the words and their order.
const relative = new Map<string, Intl.RelativeTimeFormat>();
function relativeTime(at: string, now: number, language: string, t: TFunction) {
  const minutes = Math.floor((now - Date.parse(at)) / 60000);
  if (minutes < 1) {
    return t("keys.board.justNow");
  }
  let formatter = relative.get(language);
  if (!formatter) {
    formatter = new Intl.RelativeTimeFormat(language, { numeric: "auto" });
    relative.set(language, formatter);
  }
  if (minutes < 60) {
    return formatter.format(-minutes, "minute");
  }
  if (minutes < 24 * 60) {
    return formatter.format(-Math.floor(minutes / 60), "hour");
  }
  return formatter.format(-Math.floor(minutes / (24 * 60)), "day");
}

export type RowContext = { format: Format; t: TFunction; language: string; now: number; today: string };

// keyRow is one key as the board's row shows it: its name and where its text starts and ends, what it used of its
// limit, its last 14 days, the two limits it carries, and when it was last used. A key that is off, or past its expiry,
// says so on the line under its name, since the personal list the board draws has no status column of its own.
export function keyRow(key: Key, { format, t, language, now, today }: RowContext): Row {
  const used = usedMoney(key);
  const share = limitShare(used, key.spend_limit);
  const state = key.disabled ? t("keys.board.disabled") : expired(key, now) ? t("keys.expired") : "";
  return {
    ...drawn,
    key: key.id,
    kind: "0",
    f5: key.name,
    f7: `${maskedKey(key)}${state ? ` · ${state}` : ""}`,
    f10: share >= barWarn ? "qpace w" : "qpace ",
    f11: `width: ${Math.min(100, Math.round(share * 100))}%;`,
    f13: used ? format.money(used) : "",
    f15: key.spend_limit ? ` / ${format.money(key.spend_limit)}` : t("keys.noLimit"),
    f21: spendPoints(key.daily_spend, today),
    f38: key.models ? t("keys.board.modelCount", { count: key.models.length }) : t("keys.board.allModels"),
    f39: key.ip_allow ? "qlim ok" : "qlim warn",
    f65: key.ip_allow ? t("keys.board.ipRestricted") : t("keys.board.anyIp"),
    f67: key.last_used_at ? relativeTime(key.last_used_at, now, language, t) : t("keys.board.neverUsed"),
  };
}

function expired(key: Key, now: number) {
  return key.expires_at !== undefined && Date.parse(key.expires_at) <= now;
}

// The day the spend's last entry is, as the API cuts days: UTC.
export function today(now: number) {
  return new Date(now).toISOString().slice(0, 10);
}
