import { reducedMotion, timeline } from "@axiom/fx";
import type zh from "../locales/zh";
import { firstSeen, type Script } from "./script";

// The before-production screen's script (Site.dc.html M4): the official-only switch flips and the pick moves, a line of
// the ledger lands every few seconds, and a key's month climbs to its ceiling and stops. The figures are the board's
// illustration, not an account's.
type Word = keyof typeof zh.script.trust;
type Row = { time: string; model: string; route: string; fee: number; retry: boolean; fresh: boolean };

const NL = 4;
const CEILING = 500;
const K0 = 431.6;
const CALLS: [string, string, number, boolean?][] = [
  ["claude-sonnet-5.5", "B", 0.034],
  ["gpt-6.1-sol", "A", 0.061],
  ["deepseek-v4", "C", 0.002],
  ["claude-opus-5.5", "B", 0.118],
  ["gemini-4-argon", "A", 0.019],
  ["gpt-6.1-sol", "B", 0, true],
  ["gpt-6.1-sol", "A", 0.058],
  ["kimi-k3", "B", 0.006],
];
// the three routes' input prices, the first official: the cheapest allowed one is in use
const PRICES = [21.6, 15.8, 17.2];
const clock = (d: Date) =>
  [d.getHours(), d.getMinutes(), d.getSeconds()].map((n) => String(n).padStart(2, "0")).join(":");
const rowOf = (call: (typeof CALLS)[number], at: Date, fresh: boolean): Row => ({
  time: clock(at),
  model: call[0],
  route: call[1],
  fee: call[2],
  retry: call[3] === true,
  fresh,
});

export type Input = { t: (word: Word) => string };

// the three routes: with official routes only, the third-party ones step back; the cheapest allowed one is in use
function routeValues(only: boolean, t: Input["t"]) {
  const allowed = [true, !only, !only];
  let pick = -1;
  PRICES.forEach((price, i) => {
    if (allowed[i] && (pick < 0 || price < (PRICES[pick] as number))) pick = i;
  });
  const v: Record<string, unknown> = { m4_tgA: only ? "on" : "" };
  for (let i = 0; i < 3; i++) {
    v[`m4_r${i}`] = !allowed[i] ? "off" : i === pick ? "pick" : "";
    v[`m4_r${i}n`] = i === pick ? t("inUse") : "";
  }
  return v;
}

const emptyRow = (p: string) => ({ [`${p}c`]: "off", [`${p}t`]: "", [`${p}m`]: "", [`${p}r`]: "", [`${p}f`]: "" });
const rowClass = (row: Row, last: boolean) =>
  [row.fresh && "new", row.retry && "retry", last && "old"].filter(Boolean).join(" ");

// the ledger's lines, the newest on top; a retried request is not charged
function ledgerValues(rows: Row[], t: Input["t"]) {
  const v: Record<string, unknown> = {};
  for (let i = 0; i < NL; i++) {
    const row = rows[i];
    const p = `m4_g${i}`;
    if (!row) {
      Object.assign(v, emptyRow(p));
      continue;
    }
    v[`${p}c`] = rowClass(row, i === NL - 1);
    v[`${p}t`] = row.time;
    v[`${p}m`] = row.model;
    v[`${p}r`] = row.retry ? `${row.route} ${t("timeout")}` : row.route;
    v[`${p}f`] = row.retry ? t("free") : `¥${row.fee.toFixed(3)}`;
  }
  return v;
}

// a key's month against its ceiling: at the ceiling the key stops
function keyValues(used: number, t: Input["t"]) {
  const full = used >= CEILING;
  return {
    m4_kUsed: `¥${used.toFixed(1)}`,
    m4_kW: `${Math.min(100, used / 5).toFixed(1)}%`,
    m4_kBar: full ? "full" : "",
    m4_kDot: full ? "warn" : "",
    m4_kTxtC: full ? "warnT" : "",
    m4_kState: t(full ? "stopped" : "calling"),
  };
}

export function start(stage: HTMLElement, changed: () => void): Script<Input> {
  const time = timeline(stage);
  const now = Date.now();
  const state = {
    only: false,
    rows: [3, 2, 1, 0].map((k, i) => rowOf(CALLS[k] as (typeof CALLS)[number], new Date(now - (i + 1) * 23000), false)),
    sum: 182.4,
    used: K0,
  };
  const set = (next: Partial<typeof state>) => {
    Object.assign(state, next);
    changed();
  };
  let next = 4;

  // 1: the switch flips, third-party routes step back and the pick moves to the official one
  async function flips() {
    while (await time.sleep(2800)) {
      set({ only: !state.only });
      if (!(await time.sleep(state.only ? 1400 : 0))) return;
    }
  }
  // 2: a new line lands on top every few seconds
  async function ledger() {
    while (await time.sleep(3000)) {
      const call = CALLS[next++ % CALLS.length] as (typeof CALLS)[number];
      const rows = [rowOf(call, new Date(), true), ...state.rows.map((row) => ({ ...row, fresh: false }))];
      set({ rows: rows.slice(0, NL), sum: state.sum + call[2] });
      time.sleep(900).then((ok) => ok && set({ rows: state.rows.map((row) => ({ ...row, fresh: false })) }));
    }
  }
  // 3: usage climbs to the ceiling, the key stops, then the month rolls back to where it was
  async function climb() {
    const t0 = time.now();
    const from = state.used;
    for (let p = 0; p < 1; ) {
      p = Math.min(1, (time.now() - t0) / 3600);
      set({ used: p >= 1 ? CEILING : from + (CEILING - from) * (1 - (1 - p) ** 2) });
      if (p < 1 && !(await time.sleep(60))) return false;
    }
    return true;
  }
  async function ceiling() {
    while ((await time.sleep(1200)) && (await climb()) && (await time.sleep(3200))) set({ used: K0 });
  }

  const seen = firstSeen(stage, 0.3);
  seen.promise.then(() => {
    if (reducedMotion()) return;
    flips();
    ledger();
    ceiling();
  });

  return {
    values: ({ t }) => ({
      ...routeValues(state.only, t),
      ...ledgerValues(state.rows, t),
      m4_gsum: `¥${state.sum.toFixed(2)}`,
      ...keyValues(state.used, t),
    }),
    destroy: () => {
      seen.cancel();
      time.end();
    },
  };
}
