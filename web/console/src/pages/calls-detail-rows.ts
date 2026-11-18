import type { Schemas } from "@axiom/api";
import type LogsAttempts from "@axiom/boards/AxApp/regions/LogsAttempts";
import type { TFunction } from "i18next";
import {
  callMoneyText,
  computeTimeline,
  formatMoneyOrDash,
  formatSeconds,
  type MoneyFormatter,
  usageNumber,
} from "./calls-data";

type AttemptsProps = Parameters<typeof LogsAttempts>[0];
type StepItem = AttemptsProps["rows"]["steps0"][number];
type ChargeCellItem = AttemptsProps["rows"]["chargeCells0"][number];
type SpeedMetricItem = AttemptsProps["rows"]["speedMetrics0"][number];
type IdCellItem = AttemptsProps["rows"]["idCells0"][number];

type Format = {
  money: MoneyFormatter;
  number: (n: number | bigint) => string;
  dateTime: (d: Date) => string;
};

const iconX = "m6 6 8 8M14 6l-8 8";
const iconOk = "m5 10.5 3.5 3.5L15 7";

function routeParts(route: Schemas["OwnCallRoute"] | undefined, t: TFunction): string[] {
  if (!route) return [];
  const parts: string[] = [];
  if (route.preset?.id) parts.push(`${t("callsBoard.detail.route.preset")}: ${route.preset.id}`);
  if (route.selected_endpoint) parts.push(`${t("callsBoard.detail.route.selected")}: ${route.selected_endpoint}`);
  if (route.fastest_unmeasured) parts.push(t("callsBoard.detail.route.fastestUnmeasuredYes"));
  if (route.preset_unavailable) parts.push(t("callsBoard.detail.route.presetUnavailableYes"));
  const reasons = Object.entries(route.excluded_reasons ?? {});
  if (reasons.length > 0) {
    parts.push(`${t("callsBoard.detail.route.excluded")}: ${reasons.map(([k, v]) => `${k}(${v})`).join(", ")}`);
  }
  if ((route.excluded_more ?? 0) > 0) {
    parts.push(t("callsBoard.detail.route.excludedMore", { count: route.excluded_more ?? 0 }));
  }
  return parts;
}

function buildRouteStep(call: Schemas["Call"], format: Format, t: TFunction): StepItem | null {
  const route = call.route;
  if (!route && call.error_kind !== "") return null;
  const parts = routeParts(route, t);
  const money = callMoneyText(call, t("callsBoard.list.freeTag"), format.money);
  return {
    key: "step-route-bill",
    kind: "1",
    f0: "lgstep",
    f1: "0 0 20 20",
    f2: "4",
    f3: "5",
    f4: "12",
    f5: "10",
    f6: "2",
    f7: "M4 9h12",
    f8: t("callsBoard.detail.sections.billing"),
    f9: parts.length > 0 ? `${parts.join(" · ")} · ${money}` : money,
  };
}

function attemptResultText(result: Schemas["CallAttempt"]["result"], t: TFunction) {
  if (result === "committed") return t("callsBoard.detail.attempt.committed");
  if (result === "failed") return t("callsBoard.detail.attempt.failed");
  return t("callsBoard.detail.attempt.unknown");
}

function attemptChargedText(charged: boolean | undefined, t: TFunction) {
  if (charged === undefined) return "";
  return charged ? t("callsBoard.detail.attempt.charged") : t("callsBoard.detail.attempt.uncharged");
}

function formatAttemptStep(a: Schemas["CallAttempt"], fallbackSeq: number, t: TFunction): StepItem {
  const dash = t("callsBoard.states.dash");
  const sec = t("callsBoard.states.secSuffix");
  const ok = a.result === "committed";
  const ep = a.endpoint_code || t("callsBoard.detail.attempt.noEndpoint");
  const sourceText = a.source_class ? t("callsBoard.detail.attempt.source", { source: a.source_class }) : "";
  const ttftText = a.first_token_ms
    ? t("callsBoard.detail.attempt.ttft", { val: formatSeconds(a.first_token_ms, sec, dash) })
    : "";
  const meta = [
    attemptResultText(a.result, t),
    sourceText,
    a.error_kind || "",
    ttftText,
    attemptChargedText(a.charged, t),
  ]
    .filter(Boolean)
    .join(" · ");
  return {
    key: `step-${a.sequence ?? fallbackSeq}`,
    kind: "0",
    f0: ok ? "lgstep ok" : a.result === "failed" ? "lgstep bad" : "lgstep",
    f1: "0 0 20 20",
    f2: ok ? iconOk : iconX,
    f3: t("callsBoard.detail.attempt.title", { seq: a.sequence ?? 1, endpoint: ep }),
    f4: meta,
  };
}

export function buildDetailSteps(call: Schemas["Call"], format: Format, t: TFunction): StepItem[] {
  const steps: StepItem[] = [];
  if (call.cache_hit) {
    steps.push({
      key: "step-cache",
      kind: "0",
      f0: "lgstep ok",
      f1: "0 0 20 20",
      f2: iconOk,
      f3: t("callsBoard.detail.billing.cacheHit"),
      f4: t("callsBoard.detail.attempt.cacheServed"),
    });
  } else if (call.attempts && call.attempts.length > 0) {
    const sorted = [...call.attempts].sort((x, y) => (x.sequence ?? 0) - (y.sequence ?? 0));
    for (const [i, a] of sorted.entries()) steps.push(formatAttemptStep(a, i, t));
  } else if ((call.attempt_count ?? 0) > 0) {
    steps.push({
      key: "step-summary",
      kind: "0",
      f0: call.error_kind === "" ? "lgstep ok" : "lgstep bad",
      f1: "0 0 20 20",
      f2: call.error_kind === "" ? iconOk : iconX,
      f3: t("callsBoard.detail.sections.attempts"),
      f4: t("callsBoard.detail.attempt.summaryOnly", { count: call.attempt_count ?? 0 }),
    });
  }
  const routeStep = buildRouteStep(call, format, t);
  if (routeStep) steps.push(routeStep);
  return steps;
}

function chargeItemTags(item: Schemas["ChargeItem"], t: TFunction) {
  const tags: string[] = [];
  if (item.estimated) tags.push(t("callsBoard.detail.billing.estimated"));
  if (item.fallback) tags.push(t("callsBoard.detail.billing.fallbackRate"));
  return tags.length ? ` (${tags.join(", ")})` : "";
}

function pushChargeSummaryCells(
  push: (k: string, label: string, val: string) => void,
  cd: Schemas["ChargeDetails"] | undefined,
  t: TFunction,
) {
  if (!cd) return;
  if (cd.subtotal) push("sub", t("callsBoard.detail.billing.subtotal"), cd.subtotal);
  if (cd.rounding && cd.rounding !== "0") push("rnd", t("callsBoard.detail.billing.rounding"), cd.rounding);
  if (cd.balance_adjustment && cd.balance_adjustment !== "0") {
    push("adj", t("callsBoard.detail.billing.adjustment"), cd.balance_adjustment);
  }
}

export function buildChargeCells(call: Schemas["Call"], format: Format, t: TFunction): ChargeCellItem[] {
  const cells: ChargeCellItem[] = [];
  const push = (k: string, label: string, val: string) => {
    cells.push({ key: `${k}-k`, kind: "0", f0: label }, { key: `${k}-v`, kind: "1", f0: "r", f1: val });
  };
  push(
    "free",
    t("callsBoard.detail.billing.free"),
    call.free ? t("callsBoard.detail.billing.freeYes") : t("callsBoard.detail.billing.freeNo"),
  );
  push(
    "cache",
    t("callsBoard.detail.billing.cache"),
    call.cache_hit ? t("callsBoard.detail.billing.cacheHit") : t("callsBoard.detail.billing.cacheMiss"),
  );
  if (call.price_version) push("pver", t("callsBoard.detail.billing.version"), call.price_version);
  for (const [idx, item] of (call.charge_details?.items ?? []).entries()) {
    const qty = format.number(item.quantity ?? 0);
    push(
      `ci-${idx}`,
      `${item.item ?? "item"}${chargeItemTags(item, t)}`,
      `${qty} × ${item.unit_price ?? "0"} = ${item.amount ?? "0"}`,
    );
  }
  pushChargeSummaryCells(push, call.charge_details, t);
  push("tot", t("callsBoard.detail.billing.total"), formatMoneyOrDash(call.price, format.money));
  return cells;
}

const stdUsageKeys = new Set([
  "input",
  "output",
  "input_tokens",
  "output_tokens",
  "cache_read",
  "cache_read_tokens",
  "cache_write",
  "cache_write_tokens",
]);

export function buildSpeedMetrics(call: Schemas["Call"], format: Format, t: TFunction): SpeedMetricItem[] {
  const dash = t("callsBoard.states.dash");
  const sec = t("callsBoard.states.secSuffix");
  const tl = computeTimeline(call);
  const inTok = usageNumber(call.usage, "input", "input_tokens");
  const outTok = usageNumber(call.usage, "output", "output_tokens");
  const crTok = usageNumber(call.usage, "cache_read", "cache_read_tokens");
  const cwTok = usageNumber(call.usage, "cache_write", "cache_write_tokens");
  const metrics: SpeedMetricItem[] = [
    {
      key: "sp-ttft",
      kind: "0",
      f0: t("callsBoard.detail.speed.ttft"),
      f1: formatSeconds(call.first_token_ms > 0 ? call.first_token_ms : undefined, sec, dash),
    },
    {
      key: "sp-lat",
      kind: "0",
      f0: t("callsBoard.detail.speed.latency"),
      f1: formatSeconds(call.latency_ms, sec, dash),
    },
    {
      key: "sp-tps",
      kind: "0",
      f0: t("callsBoard.detail.speed.tps"),
      f1: tl.tps ? t("callsBoard.detail.speed.tpsUnit", { n: format.number(tl.tps) }) : dash,
    },
    {
      key: "sp-tok",
      kind: "0",
      f0: t("callsBoard.col.tokens"),
      f1: `${format.number(inTok)} / ${format.number(outTok)}`,
    },
  ];
  if (crTok > 0)
    metrics.push({ key: "sp-cr", kind: "0", f0: t("callsBoard.detail.speed.cacheRead"), f1: format.number(crTok) });
  if (cwTok > 0)
    metrics.push({ key: "sp-cw", kind: "0", f0: t("callsBoard.detail.speed.cacheWrite"), f1: format.number(cwTok) });
  for (const [k, v] of Object.entries(call.usage)) {
    if (!stdUsageKeys.has(k)) metrics.push({ key: `sp-u-${k}`, kind: "0", f0: k, f1: format.number(v) });
  }
  return metrics;
}

export function buildIdCells(call: Schemas["Call"], t: TFunction): IdCellItem[] {
  const dash = t("callsBoard.states.dash");
  const cells: IdCellItem[] = [];
  const push = (k: string, label: string, val: string) => {
    cells.push({ key: `${k}-l`, kind: "0", f0: label });
    cells.push({ key: `${k}-v`, kind: "1", f0: val || dash });
  };
  push("id", t("callsBoard.detail.ids.callId"), call.id);
  push("sess", t("callsBoard.detail.ids.sessionId"), call.session_id);
  if (call.subagent_id) push("sub", t("callsBoard.detail.ids.subagentId"), call.subagent_id);
  push("rcls", t("callsBoard.detail.meta.requestClass"), call.request_class);
  push("tier", t("callsBoard.detail.meta.serviceTier"), call.service_tier);
  push("fin", t("callsBoard.detail.meta.finishReason"), call.finish_reason);
  if (call.error_kind) push("err", t("callsBoard.detail.meta.errorKind"), call.error_kind);
  push("key", t("callsBoard.detail.ids.key"), call.key_id);
  push("ws", t("callsBoard.detail.ids.workspace"), call.workspace_id);
  push("app", t("callsBoard.detail.meta.clientApp"), call.client_app);
  if (call.app_title) push("appt", t("callsBoard.detail.meta.appTitle"), call.app_title);
  if (call.app_referer) push("appr", t("callsBoard.detail.meta.appReferer"), call.app_referer);
  if (call.app_categories) push("appc", t("callsBoard.detail.meta.appCategories"), call.app_categories);
  push("ip", t("callsBoard.detail.meta.ip"), call.ip);
  return cells;
}
