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

const lgtLinkStyle =
  "min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--t2);text-decoration:none;";
const drawerBtnStyle =
  "min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;border:0;background:none;padding:0;margin:0;color:inherit;text-align:left;cursor:inherit;";
const hiddenDotStyle = "display:none;";

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

function callClientLabel(call: Schemas["Call"], t: TFunction, dash: string) {
  const attempts = call.attempt_count ?? 0;
  const base = call.client_app || dash;
  return attempts > 1 ? `${base} · ${t("callsBoard.list.fallbackAttempts", { count: attempts })}` : base;
}

function callCostLabel(call: Schemas["Call"], fmt: MoneyFormatter, t: TFunction, dash: string) {
  const moneyText = callMoneyText(call, t("callsBoard.list.freeTag"), fmt, dash);
  return call.cache_hit ? `${t("callsBoard.list.cacheTag")} · ${moneyText}` : moneyText;
}

function callRowItem(
  call: Schemas["Call"],
  selectedId: string | undefined,
  keyNames: Map<string, string>,
  format: Format,
  t: TFunction,
  hrefOf: (id: string) => string,
): ListRowItem {
  const dash = t("callsBoard.states.dash");
  const sec = t("callsBoard.states.secSuffix");
  const st = statusLabelAndClass(call, t);
  const inTok = usageNumber(call.usage, "input", "input_tokens");
  const outTok = usageNumber(call.usage, "output", "output_tokens");
  const ttftSlow = call.first_token_ms === 0 || call.first_token_ms > 5000;
  const timeText = formatClock(call.at);
  return {
    key: call.id,
    kind: "1",
    f0: call.id === selectedId ? "lgr sel" : "lgr",
    f1: "lrow",
    f2: call.id,
    f3: "lgt",
    f4: hrefOf(call.id),
    f5: lgtLinkStyle,
    f6: timeText,
    f7: st.cls,
    f8: st.text,
    f9: "lgm",
    f10: t("callsBoard.list.openDrawer", { model: call.model || dash, time: timeText }),
    f11: drawerBtnStyle,
    f12: `background: ${modelDotColor(call.model)};`,
    f13: call.model || dash,
    f14: "lgk lgx",
    f15: keyNames.get(call.key_id) ?? call.key_id,
    f16: "lgk lgx lgx2",
    f17: callClientLabel(call, t, dash),
    f18: ttftSlow ? "r lgx slow" : "r lgx",
    f19: formatSeconds(call.first_token_ms > 0 ? call.first_token_ms : undefined, sec, dash),
    f20: "r lgx lgx2",
    f21: formatSeconds(call.latency_ms, sec, dash),
    f22: "r lgx",
    f23: `${format.number(inTok)} / ${format.number(outTok)}`,
    f24: "r lgc",
    f25: callCostLabel(call, format.money, t, dash),
  };
}

export function buildCallListRows(
  calls: Schemas["Call"][],
  selectedId: string | undefined,
  keyNames: Map<string, string>,
  format: Format,
  locale: string,
  t: TFunction,
  hrefOf: (id: string) => string = (id) => `/console/calls/${encodeURIComponent(id)}`,
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
      f3: t("callsBoard.list.loadedDayCount", { n: format.number(group.calls.length) }),
    });
    for (const call of group.calls) {
      out.push(callRowItem(call, selectedId, keyNames, format, t, hrefOf));
    }
  }
  return out;
}

export function buildSummaryItems(
  aggregates: Schemas["CallAggregates"] | undefined,
  loadedCalls: Schemas["Call"][],
  loadedReady: boolean,
  format: Format,
  t: TFunction,
): SummaryItem[] {
  const dash = t("callsBoard.states.dash");
  const sec = t("callsBoard.states.secSuffix");
  const agg = summarizeAggregates(aggregates, format.money, dash);
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
      f1: aggregates ? format.number(agg.totalCalls) : dash,
      f2: aggregates ? t("callsBoard.summary.unit") : "",
    },
    { key: "sum-cost", kind: "1", f0: t("callsBoard.summary.cost"), f1: agg.costText },
    {
      key: "sum-fail",
      kind: "1",
      f0: t("callsBoard.summary.failedLoaded"),
      f1: loadedReady ? format.number(failed) : dash,
    },
    {
      key: "sum-ret",
      kind: "1",
      f0: t("callsBoard.summary.fallbackLoaded"),
      f1: loadedReady ? format.number(fallback) : dash,
    },
    {
      key: "sum-ttft",
      kind: "1",
      f0: t("callsBoard.summary.avgTtftLoaded"),
      f1: loadedReady ? avgTtft : dash,
    },
  ];
}

export function buildAggregateRows(
  aggregates: Schemas["CallAggregates"] | undefined,
  keyNames: Map<string, string>,
  groupBy: string,
  format: Format,
  t: TFunction,
  narrow = false,
): { headers0: HeaderItem[]; rows0: ListRowItem[] } {
  const dash = t("callsBoard.states.dash");
  const dimLabel = t(`callsBoard.summary.groups.${groupBy}` as "callsBoard.summary.groups.day");
  const headers0: HeaderItem[] = [
    { key: "ah-dim", kind: "0", f0: t("callsBoard.summary.colDimension") },
    { key: "ah-pad2", kind: "1", f0: "", f1: "" },
    { key: "ah-bucket", kind: "0", f0: t("callsBoard.summary.colBucket") },
    { key: "ah-pad4", kind: "1", f0: "lgx", f1: "" },
    { key: "ah-pad5", kind: "1", f0: "lgx lgx2", f1: "" },
    { key: "ah-pad6", kind: "1", f0: "lgx", f1: "" },
    { key: "ah-pad7", kind: "1", f0: "lgx lgx2", f1: "" },
    { key: "ah-calls", kind: "1", f0: "lgx r", f1: t("callsBoard.summary.colCalls") },
    { key: "ah-cost", kind: "1", f0: "r", f1: t("callsBoard.summary.colCost") },
  ];
  const rows0: ListRowItem[] = (aggregates?.data ?? []).map((row, idx) => {
    const rawBucket = row.bucket ?? dash;
    const label = groupBy === "key" ? (keyNames.get(rawBucket) ?? rawBucket) : rawBucket;
    const cost = formatMoneyOrDash(row.price, format.money, dash);
    const callNum = row.calls ? format.number(BigInt(row.calls)) : dash;
    const mobileCalls = row.calls ? t("callsBoard.sessions.callsUnit", { n: callNum }) : dash;
    return {
      key: `agg-${idx}-${rawBucket}`,
      kind: "1",
      f0: "lgr",
      f1: "agg",
      f2: rawBucket,
      f3: "lgt",
      f4: "",
      f5: lgtLinkStyle,
      f6: dimLabel,
      f7: "lgs",
      f8: narrow ? mobileCalls : "",
      f9: "lgm",
      f10: "",
      f11: drawerBtnStyle,
      f12: hiddenDotStyle,
      f13: label,
      f14: "lgx",
      f15: "",
      f16: "lgx lgx2",
      f17: "",
      f18: "lgx",
      f19: "",
      f20: "lgx lgx2",
      f21: "",
      f22: "r lgx",
      f23: callNum,
      f24: "r lgc",
      f25: cost,
    };
  });
  return { headers0, rows0 };
}

function sessionMobileText(
  s: Schemas["CallSession"],
  model: string,
  callNum: string,
  lastClock: string,
  dash: string,
  t: TFunction,
) {
  const sub = [model, s.client_app, s.last_at ? lastClock : ""].filter(Boolean).join(" · ");
  const calls = s.calls ? t("callsBoard.sessions.callsUnit", { n: callNum }) : dash;
  return { sub, calls };
}

function sessionRowDisplay(s: Schemas["CallSession"], format: Format, t: TFunction, narrow: boolean) {
  const dash = t("callsBoard.states.dash");
  const sid = s.session_id || t("callsBoard.sessions.noId");
  const model = s.main_model || dash;
  const callNum = s.calls ? format.number(BigInt(s.calls)) : dash;
  const lastClock = s.last_at ? formatClock(s.last_at) : dash;
  const mobile = sessionMobileText(s, model, callNum, lastClock, dash, t);
  return {
    sid,
    callNum,
    firstClock: s.first_at ? s.first_at.slice(0, 10) : dash,
    f5: narrow ? mobile.sub : lastClock,
    f7: narrow ? mobile.calls : s.client_app || dash,
    f12: narrow ? sid : model,
    dot: `background: ${modelDotColor(s.main_model ?? "")};`,
    cost: formatMoneyOrDash(s.price, format.money, dash),
  };
}

function sessionRowItem(
  s: Schemas["CallSession"],
  idx: number,
  format: Format,
  t: TFunction,
  narrow: boolean,
): ListRowItem {
  const d = sessionRowDisplay(s, format, t, narrow);
  return {
    key: `sess-${idx}-${d.sid}-${s.price?.currency ?? ""}`,
    kind: "1",
    f0: "lgr",
    f1: "session",
    f2: d.sid,
    f3: "lgt",
    f4: "",
    f5: lgtLinkStyle,
    f6: d.f5,
    f7: "lgs",
    f8: d.f7,
    f9: "lgm",
    f10: "",
    f11: drawerBtnStyle,
    f12: d.dot,
    f13: d.f12,
    f14: "lgk lgx",
    f15: d.sid,
    f16: "lgk lgx lgx2",
    f17: d.firstClock,
    f18: "lgx",
    f19: "",
    f20: "lgx lgx2",
    f21: "",
    f22: "r lgx",
    f23: d.callNum,
    f24: "r lgc",
    f25: d.cost,
  };
}

export function buildSessionRows(
  sessions: Schemas["CallSession"][],
  format: Format,
  t: TFunction,
  narrow = false,
): { headers0: HeaderItem[]; rows0: ListRowItem[] } {
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
  return { headers0, rows0: sessions.map((s, idx) => sessionRowItem(s, idx, format, t, narrow)) };
}
