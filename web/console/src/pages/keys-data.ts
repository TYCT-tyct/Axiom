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
// they sit in. Only the text and the classes the key's own figures pick change from one row to the next. The numbers are
// the generated region's own fields (packages/boards/src/AxApp/regions/Keys.tsx): a field the page fills in the wrong
// place puts a key's model count where its path should be, which keys-data.test.ts catches by rendering the region.
const drawn = {
  f0: "",
  f1: "qkn",
  f2: "mono",
  f4: "mono mut",
  f6: "qpc",
  f7: "qkm",
  f10: "num",
  f12: "mut",
  f14: "phx",
  f15: "qksp",
  f16: "0 0 100 20",
  f17: "none",
  f19: "phx",
  f20: "qlims",
  f21: "qlim",
  f22: "ic ic-models ",
  f23: "0 0 20 20",
  f24: "o",
  f25: "a1",
  f26: "M10 2.75 16.5 6.4v7.2L10 17.25 3.5 13.6V6.4z",
  f27: "a2",
  f28: "M3.75 6.5 10 10l6.25-3.5M10 10v7",
  f29: "f",
  f30: "a1",
  f31: "M10 2.75 16.5 6.4v7.2L10 17.25 3.5 13.6V6.4z",
  f32: "a2",
  f33: "M3.75 6.5 10 10l6.25-3.5M10 10v7",
  f36: "ic ic-security ",
  f37: "0 0 20 20",
  f38: "o",
  f39: "a1",
  f40: "4",
  f41: "9",
  f42: "12",
  f43: "8.5",
  f44: "2.25",
  f45: "a2 nf",
  f46: "M6.75 9V6.75a3.25 3.25 0 0 1 6.5 0V9",
  f47: "a3 cut",
  f48: "M10 12.25v2",
  f49: "f",
  f50: "a1",
  f51: "4",
  f52: "9",
  f53: "12",
  f54: "8.5",
  f55: "2.25",
  f56: "a2 nf",
  f57: "M6.75 9V6.75a3.25 3.25 0 0 1 6.5 0V9",
  f58: "a3 cut",
  f59: "M10 12.25v2",
  f61: "r phx",
  f63: "r",
  f64: "ic ic-chev qra",
  f65: "0 0 20 20",
  f66: "o",
  f67: "a1 nf",
  f68: "m6 8 4 4 4-4",
  f69: "f",
  f70: "a1 nf",
  f71: "m6 8 4 4 4-4",
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
// limit, its last 14 days, the two limits it carries, and when it was last used. A key past its expiry, or else one
// that is off, says so on the line under its name, since the personal list the board draws has no status column of its
// own; expiry comes first as the contract's own key status does, since switching an expired key back on cannot make it
// usable.
export function keyRow(key: Key, { format, t, language, now, today }: RowContext): Row {
  const used = usedMoney(key);
  const share = limitShare(used, key.spend_limit);
  const state = expired(key, now) ? t("keys.expired") : key.disabled ? t("keys.board.disabled") : "";
  return {
    ...drawn,
    key: key.id,
    kind: "0",
    f3: key.name,
    f5: `${maskedKey(key)}${state ? ` \u00b7 ${state}` : ""}`,
    f8: share >= barWarn ? "qpace w" : "qpace ",
    f9: `width: ${Math.min(100, Math.round(share * 100))}%;`,
    f11: used ? format.money(used) : "",
    f13: key.spend_limit ? ` / ${format.money(key.spend_limit)}${period(key, t)}` : t("keys.noLimit"),
    f18: spendPoints(key.daily_spend, today),
    f34: key.models ? t("keys.board.modelCount", { count: key.models.length }) : t("keys.board.allModels"),
    f35: key.ip_allow ? "qlim ok" : "qlim warn",
    f60: key.ip_allow ? t("keys.board.ipRestricted") : t("keys.board.anyIp"),
    f62: key.last_used_at ? relativeTime(key.last_used_at, now, language, t) : t("keys.board.neverUsed"),
  };
}

// The period a key's limit counts over, as its row says it: the limit is what the key may spend in the window its reset
// cuts, not in the calendar month the board's header claims (daily is what a new key gets).
function period(key: Key, t: TFunction) {
  return ` \u00b7 ${t(`keys.board.period.${key.limit_reset ?? "total"}`)}`;
}

function expired(key: Key, now: number) {
  return key.expires_at !== undefined && Date.parse(key.expires_at) <= now;
}

// The day the spend's last entry is, as the API cuts days: UTC.
export function today(now: number) {
  return new Date(now).toISOString().slice(0, 10);
}
