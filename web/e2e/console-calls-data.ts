import type { components } from "../packages/api/src/console.gen.ts";

type Schemas = components["schemas"];

const callId = (n: number) => `00000000-0000-4000-8000-${String(n).padStart(12, "0")}` as Schemas["CallId"];
const keyId = (n: number) => `key_${String(n).padStart(26, "0")}` as Schemas["KeyId"];
const presetId = (n: number) => `00000000-0000-4000-9000-${String(n).padStart(12, "0")}`;
const nano = (amount: string): Schemas["Money"] => ({ amount, currency: "USD" });
const usdZero: Schemas["Money"] = { amount: "0", currency: "USD" };

const models = ["anthropic/claude-sonnet-4", "openai/gpt-5", "google/gemini-2.5-pro", "deepseek/deepseek-v3"];
const apps = ["Claude Code", "Codex", "Cursor", ""];

function buildAttempts(
  failed: boolean,
  cached: boolean,
  fellBack: boolean,
  free: boolean,
  startedMs: number,
  latency: number,
): Schemas["CallAttempt"][] {
  const at = new Date(startedMs).toISOString();
  const mid = new Date(startedMs + 350).toISOString();
  const end = new Date(startedMs + latency).toISOString();
  if (cached) return [];
  if (failed) {
    return [
      {
        sequence: 1,
        endpoint_code: "ga2b3c4d2",
        source_class: "official",
        started_at: at,
        finished_at: mid,
        first_token_ms: 0,
        result: "failed",
        error_kind: "upstream_timeout",
        charged: false,
      },
      {
        sequence: 2,
        endpoint_code: "ga2b3c4d3",
        source_class: "relay",
        started_at: mid,
        finished_at: end,
        first_token_ms: 0,
        result: "failed",
        error_kind: "upstream_timeout",
        charged: false,
      },
    ];
  }
  const finalAttempt: Schemas["CallAttempt"] = {
    sequence: fellBack ? 2 : 1,
    endpoint_code: "ga2b3c4d2",
    source_class: "official",
    started_at: fellBack ? mid : at,
    finished_at: end,
    first_token_ms: 240,
    result: "committed",
    charged: !free,
  };
  if (!fellBack) return [finalAttempt];
  return [
    {
      sequence: 1,
      endpoint_code: "ga2b3c4d3",
      source_class: "relay",
      started_at: at,
      finished_at: mid,
      first_token_ms: 0,
      result: "failed",
      error_kind: "upstream_overloaded",
      charged: false,
    },
    finalAttempt,
  ];
}

function buildChargeDetails(n: number): Schemas["ChargeDetails"] {
  const sum = `0.0000${31 + n}`;
  return {
    currency: "USD",
    items: [
      { item: "input", quantity: 1200 + n, unit: "token", unit_price: "0.000003", amount: "0.000018" },
      { item: "output", quantity: 300 + n, unit: "token", unit_price: "0.000015", amount: `0.0000${13 + n}` },
    ],
    subtotal: sum,
    rounding: "0",
    charged: sum,
  };
}

function buildRoute(failed: boolean, cached: boolean, fellBack: boolean): Schemas["OwnCallRoute"] {
  return {
    preset: { id: presetId(3), version: 2 },
    selected_endpoint: failed || cached ? undefined : "ga2b3c4d2",
    fastest_unmeasured: fellBack,
    excluded_reasons: fellBack ? { health_tripped: 1 } : {},
  };
}

function callFlags(n: number) {
  const failed = n % 7 === 0;
  const free = n % 11 === 0 && !failed;
  const cached = n === 3 && !failed;
  const fellBack = !failed && !cached && n % 4 === 0;
  return { failed, free, cached, fellBack, unbilled: failed || free || cached };
}

function callTiming(n: number, failed: boolean) {
  return {
    startedMs: Date.UTC(2026, 9, 14, 8, 30) - n * 60_000,
    latency: 800 + n,
    firstTokenMs: failed ? 0 : 240,
    finishReason: failed ? "" : "stop",
    errorKind: failed ? "upstream_timeout" : "",
  };
}

export function buildMockCall(
  n: number,
  workspace: Schemas["WorkspaceId"],
  over: Partial<Schemas["Call"]> = {},
): Schemas["Call"] {
  const { failed, free, cached, fellBack, unbilled } = callFlags(n);
  const { startedMs, latency, firstTokenMs, finishReason, errorKind } = callTiming(n, failed);
  const attempts = buildAttempts(failed, cached, fellBack, free, startedMs, latency);
  const usage: Record<string, number> = failed ? {} : { input: 1200 + n, output: 300 + n, cache_read: 4000 };
  const clientApp = apps[n % apps.length] || "";
  const model = models[n % models.length] || "";
  return {
    id: callId(n),
    at: new Date(startedMs).toISOString(),
    workspace_id: workspace,
    key_id: keyId(1 + (n % 3)),
    ip: "203.0.113.7",
    client_app: clientApp,
    session_id: n % 2 === 0 ? `session-${n}` : "",
    subagent_id: n % 5 === 0 ? "agent-sub-1" : "",
    request_class: "chat",
    model,
    usage,
    service_tier: "standard",
    price: unbilled ? usdZero : nano(String(31_000 + n * 1_000)),
    price_version: "2026-10-14.1",
    free,
    cache_hit: cached,
    latency_ms: latency,
    first_token_ms: firstTokenMs,
    finish_reason: finishReason,
    error_kind: errorKind,
    attempt_count: attempts.length,
    attempts,
    route: buildRoute(failed, cached, fellBack),
    charge_details: unbilled ? undefined : buildChargeDetails(n),
    ...over,
  };
}

// lastMonth copies calls a month earlier with each charge cut so that their month spends `percent` less than these
// calls do: the overview's comparison with the same days of last month then reads +percent%.
export function lastMonth(calls: Schemas["Call"][], percent: number): Schemas["Call"][] {
  return calls.map((call) => {
    const at = new Date(call.at);
    at.setUTCMonth(at.getUTCMonth() - 1);
    const amount = ((BigInt(call.price.amount) * 100n) / BigInt(100 + percent)).toString();
    const id = call.id.replace(/^0{8}/, "11111111") as Schemas["CallId"];
    return { ...call, id, at: at.toISOString(), price: { ...call.price, amount } };
  });
}
