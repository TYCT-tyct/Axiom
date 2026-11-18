import type { Schemas } from "@axiom/api";
import type Logs from "@axiom/boards/AxApp/regions/Logs";
import type { TFunction } from "i18next";
import {
  callMoneyText,
  callStatusKind,
  formatClock,
  formatDayHeading,
  formatMoneyOrDash,
  formatSeconds,
  groupCallsByDay,
  type MoneyFormatter,
  modelDotColor,
  summarizeAggregates,
  usageNumber,
} from "./calls-data";

type LogsProps = Parameters<typeof Logs>[0];
export type HeaderItem = LogsProps["rows"]["headers0"][number];
export type ListRowItem = LogsProps["rows"]["rows0"][number];
export type SummaryItem = LogsProps["rows"]["summaryItems0"][number];

type Format = {
  money: MoneyFormatter;
  number: (n: number | bigint) => string;
  dateTime: (d: Date) => string;
};

export function callHeaders(t: TFunction): HeaderItem[] {
  return [
    { key: "h-time", kind: "0", f0: t("callsBoard.col.time") },
    { key: "h-status", kind: "0", f0: t("callsBoard.col.status") },
    { key: "h-model", kind: "0", f0: t("callsBoard.col.model") },
    { key: "h-key", kind: "1", f0: "lgx", f1: t("callsBoard.col.key") },
    { key: "h-client", kind: "1", f0: "lgx lgx2", f1: t("callsBoard.col.client") },
    { key: "h-ttft", kind: "1", f0: "lgx r", f1: t("callsBoard.col.ttft") },
    { key: "h-lat", kind: "1", f0: "lgx lgx2 r", f1: t("callsBoard.col.latency") },
    { key: "h-tok", kind: "1", f0: "lgx r", f1: t("callsBoard.col.tokens") },
    { key: "h-cost", kind: "1", f0: "r", f1: t("callsBoard.col.cost") },
  ];
}

export function statusLabelAndClass(call: Schemas["Call"], t: TFunction) {
  const kind = callStatusKind(call);
  const count = call.attempt_count ?? 1;
  if (kind === "fail") {
    return {
      cls: "lgs fail",
      text: t("callsBoard.status.fail"),
      badge: "color: var(--badT);",
      dot: "background: var(--bad);",
    };
  }
  if (kind === "retry") {
    const label = `${t("callsBoard.status.retry")} (${count})`;
    return { cls: "lgs retry", text: label, badge: "color: var(--t2);", dot: "background: var(--warn);" };
  }
  if (kind === "cancelled") {
    return {
      cls: "lgs",
      text: t("callsBoard.status.cancelled"),
      badge: "color: var(--t2);",
      dot: "background: var(--t3);",
    };
  }
  return { cls: "lgs ok", text: t("callsBoard.status.ok"), badge: "color: var(--t2);", dot: "background: var(--ok);" };
}

function callRowItem(
  call: Schemas["Call"],
  selectedId: string | undefined,
  keyNames: Map<string, string>,
  format: Format,
  t: TFunction,
): ListRowItem {
  const dash = t("callsBoard.states.dash");
  const sec = t("callsBoard.states.secSuffix");
  const st = statusLabelAndClass(call, t);
  const inTok = usageNumber(call.usage, "input", "input_tokens");
  const outTok = usageNumber(call.usage, "output", "output_tokens");
  const ttftSlow = call.first_token_ms === 0 || call.first_token_ms > 5000;
  const attemptsNote =
    (call.attempt_count ?? 0) > 1
      ? ` · ${t("callsBoard.list.fallbackAttempts", { count: call.attempt_count ?? 0 })}`
      : "";
  const clientText = `${call.client_app || dash}${attemptsNote}`;
  const costText = call.cache_hit
    ? `${t("callsBoard.list.cacheTag")} · ${formatMoneyOrDash(call.price, format.money, dash)}`
    : callMoneyText(call, t("callsBoard.list.freeTag"), format.money);
  return {
    key: call.id,
    kind: "1",
    f0: call.id === selectedId ? "lgr sel" : "lgr",
    f1: "lrow",
    f2: call.id,
    f3: "lgt",
    f4: `/console/calls/${encodeURIComponent(call.id)}`,
    f5: formatClock(call.at),
    f6: st.cls,
    f7: st.text,
    f8: "lgm",
    f9: `background: ${modelDotColor(call.model)};`,
    f10: call.model || dash,
    f11: "lgk lgx",
    f12: keyNames.get(call.key_id) ?? call.key_id,
    f13: "lgk lgx lgx2",
    f14: clientText,
    f15: ttftSlow ? "r lgx slow" : "r lgx",
    f16: formatSeconds(call.first_token_ms > 0 ? call.first_token_ms : undefined, sec, dash),
    f17: "r lgx lgx2",
    f18: formatSeconds(call.latency_ms, sec, dash),
    f19: "r lgx",
    f20: `${format.number(inTok)} / ${format.number(outTok)}`,
    f21: "r lgc",
    f22: costText,
  };
}

export function buildCallListRows(
  calls: Schemas["Call"][],
  selectedId: string | undefined,
  keyNames: Map<string, string>,
  format: Format,
  locale: string,
  t: TFunction,
): ListRowItem[] {
  const out: ListRowItem[] = [];
  const groups = groupCallsByDay(calls);
  const labels = { today: t("callsBoard.states.dayToday"), yesterday: t("callsBoard.states.dayYesterday") };
  for (const group of groups) {
    const day = formatDayHeading(group.dayKey, labels, locale);
    out.push({
      key: `day-${group.dayKey}`,
      kind: "0",
      f0: "lgday",
      f1: day.primary,
      f2: day.secondary,
      f3: `${format.number(group.calls.length)}${t("callsBoard.summary.unit")}`,
    });
    for (const call of group.calls) {
      out.push(callRowItem(call, selectedId, keyNames, format, t));
    }
  }
  return out;
}

export function buildSummaryItems(
  aggregates: Schemas["CallAggregates"] | undefined,
  loadedCalls: Schemas["Call"][],
  format: Format,
  t: TFunction,
): SummaryItem[] {
  const dash = t("callsBoard.states.dash");
  const sec = t("callsBoard.states.secSuffix");
  const agg = summarizeAggregates(aggregates, format.money, dash);
  const count = aggregates ? agg.totalCalls : BigInt(loadedCalls.length);
  const failed = loadedCalls.filter((c) => c.error_kind !== "").length;
  const fallback = loadedCalls.filter((c) => (c.attempt_count ?? 1) > 1).length;
  const ttfts = loadedCalls.filter((c) => c.first_token_ms > 0);
  const avgTtft = ttfts.length
    ? formatSeconds(ttfts.reduce((s, c) => s + c.first_token_ms, 0) / ttfts.length, sec, dash)
    : dash;
  return [
    {
      key: "sum-total",
      kind: "0",
      f0: t("callsBoard.summary.total"),
      f1: format.number(count),
      f2: t("callsBoard.summary.unit"),
    },
    { key: "sum-cost", kind: "1", f0: t("callsBoard.summary.cost"), f1: agg.costText },
    { key: "sum-fail", kind: "1", f0: t("callsBoard.summary.failed"), f1: format.number(failed) },
    { key: "sum-ret", kind: "1", f0: t("callsBoard.summary.fallback"), f1: format.number(fallback) },
    { key: "sum-ttft", kind: "1", f0: t("callsBoard.summary.avgTtft"), f1: avgTtft },
  ];
}

export function buildAggregateRows(
  aggregates: Schemas["CallAggregates"] | undefined,
  keyNames: Map<string, string>,
  groupBy: string,
  format: Format,
  t: TFunction,
): { headers0: HeaderItem[]; rows0: ListRowItem[] } {
  const dash = t("callsBoard.states.dash");
  const headers0: HeaderItem[] = [
    { key: "ah-bucket", kind: "0", f0: t("callsBoard.summary.colBucket") },
    { key: "ah-pad2", kind: "1", f0: "lgx", f1: "" },
    { key: "ah-pad3", kind: "1", f0: "lgx", f1: "" },
    { key: "ah-pad4", kind: "1", f0: "lgx", f1: "" },
    { key: "ah-pad5", kind: "1", f0: "lgx lgx2", f1: "" },
    { key: "ah-pad6", kind: "1", f0: "lgx", f1: "" },
    { key: "ah-pad7", kind: "1", f0: "lgx lgx2", f1: "" },
    { key: "ah-calls", kind: "1", f0: "r", f1: t("callsBoard.summary.colCalls") },
    { key: "ah-cost", kind: "1", f0: "r", f1: t("callsBoard.summary.colCost") },
  ];
  const rows0: ListRowItem[] = (aggregates?.data ?? []).map((row, idx) => {
    const rawBucket = row.bucket ?? dash;
    const label = groupBy === "key" ? (keyNames.get(rawBucket) ?? rawBucket) : rawBucket;
    const cost = formatMoneyOrDash(row.price, format.money, dash);
    return {
      key: `agg-${idx}-${rawBucket}`,
      kind: "1",
      f0: "lgr",
      f1: "agg",
      f2: rawBucket,
      f3: "lgt",
      f4: "#summary",
      f5: label,
      f6: "lgx",
      f7: "",
      f8: "lgx",
      f9: "background: transparent;",
      f10: "",
      f11: "lgx",
      f12: "",
      f13: "lgx lgx2",
      f14: "",
      f15: "lgx",
      f16: "",
      f17: "lgx lgx2",
      f18: "",
      f19: "r",
      f20: row.calls ? format.number(BigInt(row.calls)) : dash,
      f21: "r lgc",
      f22: cost,
    };
  });
  return { headers0, rows0 };
}

export function buildSessionRows(
  sessions: Schemas["CallSession"][],
  format: Format,
  t: TFunction,
): { headers0: HeaderItem[]; rows0: ListRowItem[] } {
  const dash = t("callsBoard.states.dash");
  const headers0: HeaderItem[] = [
    { key: "sh-last", kind: "0", f0: t("callsBoard.sessions.col.lastAt") },
    { key: "sh-client", kind: "0", f0: t("callsBoard.sessions.col.client") },
    { key: "sh-model", kind: "0", f0: t("callsBoard.sessions.col.model") },
    { key: "sh-sid", kind: "1", f0: "lgx", f1: t("callsBoard.sessions.col.session") },
    { key: "sh-first", kind: "1", f0: "lgx lgx2", f1: t("callsBoard.sessions.col.firstAt") },
    { key: "sh-empty1", kind: "1", f0: "lgx r", f1: "" },
    { key: "sh-empty2", kind: "1", f0: "lgx lgx2 r", f1: "" },
    { key: "sh-calls", kind: "1", f0: "lgx r", f1: t("callsBoard.sessions.col.calls") },
    { key: "sh-cost", kind: "1", f0: "r", f1: t("callsBoard.sessions.col.cost") },
  ];
  const rows0: ListRowItem[] = sessions.map((s, idx) => {
    const sid = s.session_id || t("callsBoard.sessions.noId");
    const cost = formatMoneyOrDash(s.price, format.money, dash);
    return {
      key: `sess-${idx}-${sid}`,
      kind: "1",
      f0: "lgr",
      f1: "session",
      f2: s.client_app ?? "",
      f3: "lgt",
      f4: s.client_app ? `/console/calls?client_app=${encodeURIComponent(s.client_app)}` : "/console/calls",
      f5: s.last_at ? format.dateTime(new Date(s.last_at)) : dash,
      f6: "lgk",
      f7: s.client_app || dash,
      f8: "lgm",
      f9: `background: ${modelDotColor(s.main_model ?? "")};`,
      f10: s.main_model || dash,
      f11: "lgk lgx",
      f12: sid,
      f13: "lgk lgx lgx2",
      f14: s.first_at ? format.dateTime(new Date(s.first_at)) : dash,
      f15: "lgx",
      f16: "",
      f17: "lgx lgx2",
      f18: "",
      f19: "r lgx",
      f20: s.calls ? format.number(BigInt(s.calls)) : dash,
      f21: "r lgc",
      f22: cost,
    };
  });
  return { headers0, rows0 };
}
