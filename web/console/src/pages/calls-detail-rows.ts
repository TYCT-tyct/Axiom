import type { Schemas } from "@axiom/api";
import { systemPreset } from "@axiom/app";
import type LogsAttempts from "@axiom/boards/AxApp/regions/LogsAttempts";
import type { TFunction } from "i18next";
import {
  callMoneyText,
  computeTimeline,
  formatDecimalMoney,
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
const idLabelStyle = "color:var(--t2);font-size:12px;line-height:20px;display:inline-flex;align-items:center;gap:4px;";

function routeParts(route: Schemas["OwnCallRoute"] | undefined, t: TFunction): string[] {
  if (!route) return [];
  const parts: string[] = [];
  if (route.preset?.id) {
    const presetLabel = route.preset.id === systemPreset ? t("routingPresets.key.system") : route.preset.id;
    parts.push(`${t("callsBoard.detail.route.preset")}: ${presetLabel}`);
  }
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
  if (charged === undefined) return t("callsBoard.detail.attempt.chargedUnknown");
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
    const sorted = [...call.attempts].sort((x, y) => aSeq(x) - aSeq(y));
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

const aSeq = (a: Schemas["CallAttempt"]) => a.sequence ?? 0;

function chargeItemTags(item: Schemas["ChargeItem"], t: TFunction) {
  const tags: string[] = [];
  if (item.estimated) tags.push(t("callsBoard.detail.billing.estimated"));
  if (item.fallback) tags.push(t("callsBoard.detail.billing.fallbackRate"));
  return tags.length ? ` (${tags.join(", ")})` : "";
}

function chargeItemName(item: string | undefined, t: TFunction) {
  if (!item) return "item";
  const known = ["input", "output", "cache_read", "cache_write", "request"];
  return known.includes(item)
    ? t(`callsBoard.detail.billing.items.${item}` as "callsBoard.detail.billing.items.input")
    : item;
}

function chargeItemUnit(unit: Schemas["ChargeItem"]["unit"], t: TFunction) {
  if (!unit) return "";
  return t(`callsBoard.detail.billing.units.${unit}` as "callsBoard.detail.billing.units.token");
}

type ChargeCtx = {
  pushRow: (k: string, label: string, formula: string, val: string, total?: boolean) => void;
  cd: Schemas["ChargeDetails"] | undefined;
  currency: string | undefined;
  format: Format;
  t: TFunction;
};

function pushChargeSummaryCells({ pushRow, cd, currency, format, t }: ChargeCtx) {
  if (!cd) return;
  if (cd.subtotal) {
    pushRow(
      "sub",
      t("callsBoard.detail.billing.subtotal"),
      "",
      formatDecimalMoney(cd.subtotal, currency, format.money),
    );
  }
  if (cd.rounding && cd.rounding !== "0") {
    pushRow(
      "rnd",
      t("callsBoard.detail.billing.rounding"),
      "",
      formatDecimalMoney(cd.rounding, currency, format.money),
    );
  }
  if (cd.balance_adjustment && cd.balance_adjustment !== "0") {
    pushRow(
      "adj",
      t("callsBoard.detail.billing.adjustment"),
      "",
      formatDecimalMoney(cd.balance_adjustment, currency, format.money),
    );
  }
}

function pushChargeItemRows({ pushRow, cd, currency, format, t }: ChargeCtx) {
  for (const [idx, item] of (cd?.items ?? []).entries()) {
    const qty = format.number(item.quantity ?? 0);
    const unitText = chargeItemUnit(item.unit, t);
    const formula = `${qty} × ${item.unit_price ?? "0"}${unitText ? ` / ${unitText}` : ""}`;
    pushRow(
      `ci-${idx}`,
      `${chargeItemName(item.item, t)}${chargeItemTags(item, t)}`,
      formula,
      formatDecimalMoney(item.amount, currency, format.money),
    );
  }
}

export function buildChargeCells(call: Schemas["Call"], format: Format, t: TFunction): ChargeCellItem[] {
  const cells: ChargeCellItem[] = [];
  const pushRow = (k: string, label: string, formula: string, val: string, total = false) => {
    cells.push(
      total ? { key: `${k}-k`, kind: "1", f0: "lgkt", f1: label } : { key: `${k}-k`, kind: "0", f0: label },
      { key: `${k}-x`, kind: "1", f0: total ? "lgkt lgkx" : "lgkx", f1: formula },
      { key: `${k}-v`, kind: "1", f0: total ? "lgkt lgkvv" : "lgkvv", f1: val },
    );
  };
  const cd = call.charge_details;
  const currency = cd?.currency || call.price.currency;
  const ctx: ChargeCtx = { pushRow, cd, currency, format, t };
  pushRow(
    "free",
    t("callsBoard.detail.billing.free"),
    "",
    t(call.free ? "callsBoard.detail.billing.freeYes" : "callsBoard.detail.billing.freeNo"),
  );
  pushRow(
    "cache",
    t("callsBoard.detail.billing.cache"),
    "",
    t(call.cache_hit ? "callsBoard.detail.billing.cacheHit" : "callsBoard.detail.billing.cacheMiss"),
  );
  if (call.price_version) pushRow("pver", t("callsBoard.detail.billing.version"), "", call.price_version);
  pushChargeItemRows(ctx);
  pushChargeSummaryCells(ctx);
  const totalVal = cd?.charged
    ? formatDecimalMoney(cd.charged, currency, format.money)
    : formatMoneyOrDash(call.price, format.money);
  pushRow(
    "tot",
    t("callsBoard.detail.billing.total"),
    cd?.estimated ? t("callsBoard.detail.billing.estimated") : "",
    totalVal,
    true,
  );
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
    if (!stdUsageKeys.has(k) && v > 0) metrics.push({ key: `sp-u-${k}`, kind: "0", f0: k, f1: format.number(v) });
  }
  return metrics;
}

export function buildIdCells(call: Schemas["Call"], t: TFunction): IdCellItem[] {
  const dash = t("callsBoard.states.dash");
  const cells: IdCellItem[] = [];
  const push = (k: string, label: string, val: string, copyable = false) => {
    const canCopy = copyable && Boolean(val);
    cells.push({
      key: `${k}-l`,
      kind: "0",
      f0: canCopy ? "1" : "0",
      f1: canCopy ? val : "",
      f2: canCopy ? t("callsBoard.detail.copyValue", { label }) : "",
      f3: idLabelStyle,
      f4: label,
    });
    cells.push({ key: `${k}-v`, kind: "1", f0: val || dash });
  };
  push("id", t("callsBoard.detail.ids.callId"), call.id, true);
  push("sess", t("callsBoard.detail.ids.sessionId"), call.session_id, true);
  if (call.subagent_id) push("sub", t("callsBoard.detail.ids.subagentId"), call.subagent_id, true);
  push("rcls", t("callsBoard.detail.meta.requestClass"), call.request_class);
  push("tier", t("callsBoard.detail.meta.serviceTier"), call.service_tier);
  push("fin", t("callsBoard.detail.meta.finishReason"), call.finish_reason);
  if (call.error_kind) push("err", t("callsBoard.detail.meta.errorKind"), call.error_kind);
  push("key", t("callsBoard.detail.ids.key"), call.key_id, true);
  push("ws", t("callsBoard.detail.ids.workspace"), call.workspace_id, true);
  push("app", t("callsBoard.detail.meta.clientApp"), call.client_app);
  if (call.app_title) push("appt", t("callsBoard.detail.meta.appTitle"), call.app_title);
  if (call.app_referer) push("appr", t("callsBoard.detail.meta.appReferer"), call.app_referer);
  if (call.app_categories) push("appc", t("callsBoard.detail.meta.appCategories"), call.app_categories);
  push("ip", t("callsBoard.detail.meta.ip"), call.ip);
  return cells;
}
