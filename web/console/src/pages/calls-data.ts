import { ApiError, type Schemas } from "@axiom/api";
import type { CallFilters } from "../queries";

export type RangePreset = "all" | "today" | "d7" | "d30" | "month" | "custom";
export type PresetChoice = Exclude<RangePreset, "all" | "custom">;

export function dateRangeForPreset(preset: Exclude<RangePreset, "custom">, now = new Date()) {
  if (preset === "all") {
    return { since: undefined, until: undefined };
  }
  const y = now.getFullYear();
  const m = now.getMonth();
  const d = now.getDate();
  const start =
    preset === "today"
      ? new Date(y, m, d)
      : preset === "d7"
        ? new Date(y, m, d - 6)
        : preset === "d30"
          ? new Date(y, m, d - 29)
          : new Date(y, m, 1);
  return { since: start.toISOString(), until: undefined };
}

export function detectRangePreset(since?: string, until?: string, now = new Date()): RangePreset {
  if (!since && !until) return "all";
  if (!since || until) return "custom";
  const ts = Date.parse(since);
  if (Number.isNaN(ts)) return "custom";
  for (const preset of ["today", "d7", "d30", "month"] as const) {
    if (ts === Date.parse(dateRangeForPreset(preset, now).since ?? "")) return preset;
  }
  return "custom";
}

export function resolveRangePreset(
  since?: string,
  until?: string,
  preset?: PresetChoice,
  now = new Date(),
): RangePreset {
  if (!since && !until) return "all";
  if (!since || until) return "custom";
  const ts = Date.parse(since);
  if (Number.isNaN(ts)) return "custom";
  if (preset && ts === Date.parse(dateRangeForPreset(preset, now).since ?? "")) return preset;
  return detectRangePreset(since, until, now);
}

export function formatSeconds(ms: number | null | undefined, suffix: string, dash: string) {
  if (ms === undefined || ms === null || Number.isNaN(ms)) return dash;
  const sec = ms / 1000;
  return `${sec < 10 ? sec.toFixed(2) : sec.toFixed(1)}${suffix}`;
}

function dayKey(iso: string) {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso.slice(0, 10);
  const y = date.getFullYear();
  const m = String(date.getMonth() + 1).padStart(2, "0");
  const d = String(date.getDate()).padStart(2, "0");
  return `${y}-${m}-${d}`;
}

export function formatClock(iso: string) {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso.slice(11, 19);
  const hh = String(date.getHours()).padStart(2, "0");
  const mm = String(date.getMinutes()).padStart(2, "0");
  const ss = String(date.getSeconds()).padStart(2, "0");
  return `${hh}:${mm}:${ss}`;
}

export function formatDayHeading(
  key: string,
  labels: { today: string; yesterday: string },
  locale: string,
  now = new Date(),
) {
  const todayKey = dayKey(now.toISOString());
  const yest = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1);
  const yestKey = dayKey(yest.toISOString());
  const parts = key.split("-").map(Number);
  const dateObj = parts.length === 3 ? new Date(parts[0] ?? 0, (parts[1] ?? 1) - 1, parts[2] ?? 1) : null;
  const shortDate =
    dateObj && !Number.isNaN(dateObj.getTime())
      ? new Intl.DateTimeFormat(locale, { month: "short", day: "numeric" }).format(dateObj)
      : key;
  if (key === todayKey) return { primary: labels.today, secondary: shortDate };
  if (key === yestKey) return { primary: labels.yesterday, secondary: shortDate };
  return { primary: shortDate, secondary: "" };
}

export function groupCallsByDay(calls: Schemas["Call"][]) {
  const groups: Array<{ dayKey: string; calls: Schemas["Call"][] }> = [];
  for (const call of calls) {
    const k = dayKey(call.at);
    const last = groups.at(-1);
    if (last && last.dayKey === k) last.calls.push(call);
    else groups.push({ dayKey: k, calls: [call] });
  }
  return groups;
}

export function callStatusKind(call: Schemas["Call"]): "fail" | "retry" | "cancelled" | "ok" {
  if (call.error_kind !== "") return "fail";
  if (call.finish_reason === "cancelled") return "cancelled";
  if ((call.attempt_count ?? 1) > 1) return "retry";
  return "ok";
}

export function usageNumber(usage: Record<string, number>, ...keys: string[]) {
  for (const key of keys) {
    const val = usage[key];
    if (typeof val === "number") return val;
  }
  return 0;
}

export type ExportErrorKind = "rows_limit" | "window_limit" | "invalid_field" | "other";

export function classifyExportError(error: unknown, filters: CallFilters, now = new Date()): ExportErrorKind {
  if (!(error instanceof ApiError) || error.status !== 400 || error.code !== "invalid_request") {
    return "other";
  }
  const field = error.fields.length === 1 ? error.fields[0] : undefined;
  if (field?.pointer === "since") {
    if (/^more than \d+ calls; narrow the window or filters$/.test(field.message)) return "rows_limit";
    // The API reports inverted and overlong windows with the same reason.
    if (field.message === "choose a nonempty window of at most 31 days" && filters.since) {
      const until = filters.until ? Date.parse(filters.until) : now.getTime();
      if (Date.parse(filters.since) < until) return "window_limit";
    }
  }
  return "invalid_field";
}

export function decimalToNano(amount: string): string | null {
  const m = /^(-?)(\d+)(?:\.(\d+))?$/.exec(amount.trim());
  if (!m) return null;
  const [, sign = "", whole = "0", frac = ""] = m;
  if (frac.length > 9) return null;
  const raw = `${whole}${frac.padEnd(9, "0")}`.replace(/^0+(?=\d)/, "");
  return raw === "0" ? "0" : `${sign}${raw}`;
}

export function formatDecimalMoney(
  amount: string | undefined,
  currency: string | undefined,
  fmt: MoneyFormatter,
  dash = "—",
) {
  if (!amount) return dash;
  if (!currency) return amount;
  const nano = decimalToNano(amount);
  if (nano !== null) return fmt({ amount: nano, currency });
  return `${currency} ${amount}`;
}

export function modelDotColor(model: string) {
  let hash = 0;
  for (let i = 0; i < model.length; i += 1) {
    hash = (hash * 31 + model.charCodeAt(i)) >>> 0;
  }
  return `var(--c${hash % 5})`;
}

export type MoneyFormatter = (m: { amount: string; currency: string }) => string;

export function formatMoneyOrDash(
  price: { amount?: string | number; currency?: string } | undefined,
  fmt: MoneyFormatter,
  dash = "—",
) {
  return price?.amount != null && price.currency
    ? fmt({ amount: String(price.amount), currency: price.currency })
    : dash;
}

export function callMoneyText(call: Schemas["Call"], freeLabel: string, fmt: MoneyFormatter, dash = "—") {
  const cd = call.charge_details;
  const formatted =
    cd?.charged != null && cd.charged !== ""
      ? formatDecimalMoney(cd.charged, cd.currency || call.price.currency, fmt, dash)
      : formatMoneyOrDash(call.price, fmt, dash);
  return call.free ? `${freeLabel} · ${formatted}` : formatted;
}

export function summarizeAggregates(
  aggregates: Schemas["CallAggregates"] | undefined,
  fmt: MoneyFormatter,
  dash = "—",
) {
  const rows = aggregates?.data ?? [];
  let totalCalls = 0n;
  const totalsByCurrency = new Map<string, bigint>();
  for (const row of rows) {
    if (row.calls) totalCalls += BigInt(row.calls);
    if (row.price?.amount && row.price.currency) {
      const prev = totalsByCurrency.get(row.price.currency) ?? 0n;
      totalsByCurrency.set(row.price.currency, prev + BigInt(row.price.amount));
    }
  }
  const costText =
    totalsByCurrency.size === 0
      ? dash
      : [...totalsByCurrency.entries()]
          .map(([currency, amount]) => fmt({ amount: amount.toString(), currency }))
          .join(" + ");
  return { totalCalls, costText, rows };
}

export function computeTimeline(call: Schemas["Call"]) {
  const totalSec = Math.max(call.latency_ms / 1000, 0.01);
  const scaleSec = Math.max(10, Math.ceil(totalSec / 5) * 5);
  const attempts = [...(call.attempts ?? [])].sort((a, b) => (a.sequence ?? 0) - (b.sequence ?? 0));
  const failedMs = attempts
    .filter((a) => a.result === "failed" && a.started_at && a.finished_at)
    .reduce((sum, a) => sum + Math.max(0, Date.parse(a.finished_at ?? "") - Date.parse(a.started_at ?? "")), 0);
  const ttftMs = call.first_token_ms > 0 ? Math.min(call.first_token_ms, call.latency_ms) : 0;
  const waitMs = Math.max(0, ttftMs - Math.min(ttftMs, failedMs));
  const genMs = call.error_kind === "" && !call.cache_hit ? Math.max(0, call.latency_ms - failedMs - waitMs) : 0;
  const pct = (ms: number) => Math.min(100, Math.max(0, (ms / 1000 / scaleSec) * 100));
  const outTok = usageNumber(call.usage, "output", "output_tokens");
  const tps = genMs > 50 && outTok > 0 ? Math.round((outTok * 1000) / genMs) : null;
  return {
    scaleSec,
    failPct: pct(failedMs),
    waitPct: pct(waitMs),
    genPct: pct(genMs),
    tps,
    attempts,
  };
}

const trimmed = (value?: string) => {
  const s = value?.trim();
  return s ? s : undefined;
};

export function cleanSearchFilters(
  next: CallFilters & { preset?: PresetChoice; group_by?: "day" | "model" | "key"; tab?: "sessions"; call?: string },
) {
  return {
    since: trimmed(next.since),
    until: trimmed(next.until),
    preset: next.preset,
    model: trimmed(next.model),
    client_app: trimmed(next.client_app),
    key_id: trimmed(next.key_id),
    workspace_id: trimmed(next.workspace_id),
    status: next.status || undefined,
    fell_back: next.fell_back ? true : undefined,
    request_id: trimmed(next.request_id),
    group_by: next.group_by && next.group_by !== "day" ? next.group_by : undefined,
    tab: next.tab === "sessions" ? ("sessions" as const) : undefined,
    call: trimmed(next.call),
  };
}

export function hasActiveCallFilters(filters: CallFilters) {
  return (
    ["since", "until", "model", "client_app", "key_id", "workspace_id", "status", "fell_back", "request_id"] as const
  ).some((k) => Boolean(filters[k]));
}
