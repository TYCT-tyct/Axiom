import type { paths } from "../packages/api/src/console.gen.ts";
import type { MockState, Schemas } from "./console-data.ts";
import { type Ctx as CoreCtx, createRoute, fail, type Handler, paged } from "./mock.ts";

const base = "/api/console/v1";
const route = createRoute<paths, MockState>();
type Ctx<K extends keyof paths, M extends "get" | "post" | "put" | "delete"> = CoreCtx<paths, K, M, MockState>;
const signedIn = { level: "signed_in" } as const;

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const keyIdPattern = /^key_[0-9A-Za-z]+$/;
const wsIdPattern = /^ws_[0-9A-Za-z]+$/;
const oct15Ms = Date.UTC(2026, 9, 15, 0, 0, 0);
const sessionClocks = new WeakMap<MockState, number>();

function failFields(fields: Schemas["ErrorField"][]) {
  return {
    status: 400 as const,
    json: { error: { code: "invalid_request", message: "invalid_request", fields } } satisfies Schemas["Error"],
  };
}

function validateCallFilters(query: URLSearchParams): Schemas["ErrorField"][] {
  const fields: Schemas["ErrorField"][] = [];
  for (const name of ["since", "until"] as const) {
    const raw = query.get(name);
    if (raw && Number.isNaN(Date.parse(raw))) fields.push({ pointer: name, message: "expected an RFC3339 instant" });
  }
  const idRules = [
    ["request_id", uuidPattern],
    ["key_id", keyIdPattern],
    ["workspace_id", wsIdPattern],
  ] as const;
  for (const [name, pattern] of idRules) {
    const val = query.get(name);
    if (val && !pattern.test(val)) fields.push({ pointer: name, message: "invalid identifier" });
  }
  return fields;
}

function matchesExactFilters(call: Schemas["Call"], query: URLSearchParams) {
  const fields = ["model", "client_app", "key_id", "workspace_id", "request_id"] as const;
  return fields.every((field) => {
    const val = query.get(field);
    if (!val) return true;
    return (field === "request_id" ? call.id : call[field]) === val;
  });
}

function matchingCalls(calls: Schemas["Call"][], query: URLSearchParams) {
  const since = query.get("since");
  const until = query.get("until");
  const status = query.get("status") as Schemas["CallStatus"] | null;
  const fellBack = query.get("fell_back") === "true" || query.get("fell_back") === "1";
  return calls.filter(
    (call) =>
      matchesExactFilters(call, query) &&
      (!since || call.at >= new Date(since).toISOString()) &&
      (!until || call.at < new Date(until).toISOString()) &&
      (!status || (call.error_kind !== "") === (status === "error")) &&
      (!fellBack || (call.attempt_count ?? 1) > 1),
  );
}

function defaultUntilFor(query: URLSearchParams, tickSec = 0) {
  const rawSince = query.get("since");
  const baseMs = rawSince && Date.parse(rawSince) >= oct15Ms ? Date.now() : oct15Ms;
  return new Date(baseMs + tickSec * 1000).toISOString();
}

function resolveBoundedWindow(query: URLSearchParams, tickSec = 0) {
  const resolvedUntil = query.get("until") || defaultUntilFor(query, tickSec);
  const untilMs = Date.parse(resolvedUntil);
  const resolvedSince = query.get("since") || new Date(untilMs - 30 * 86_400_000).toISOString();
  const sinceMs = Date.parse(resolvedSince);
  if (!(sinceMs < untilMs) || untilMs - sinceMs > 31 * 86_400_000) return null;
  const bounded = new URLSearchParams(query);
  bounded.set("since", resolvedSince);
  bounded.set("until", resolvedUntil);
  return { since: resolvedSince, until: resolvedUntil, bounded };
}

function listCallSummary(call: Schemas["Call"]): Schemas["Call"] {
  const { attempts: _attempts, route: _route, ...summary } = call;
  return summary;
}

function listCalls({ query, state }: Ctx<"/api/console/v1/calls", "get">) {
  const bad = validateCallFilters(query);
  if (bad.length > 0) return failFields(bad);
  const status = query.get("status") as Schemas["CallStatus"] | null;
  const fellBack = query.get("fell_back") === "true" || query.get("fell_back") === "1";
  const page = paged(matchingCalls(state.calls, query), query);
  return page
    ? ({
        status: 200,
        json: {
          ...page,
          data: page.data.map(listCallSummary),
          status_filter: status ?? undefined,
          fell_back_filter: fellBack ? true : undefined,
        },
      } as const)
    : fail(400, "invalid_request");
}

type SessionEntry = {
  session_id: string;
  currency: string;
  client_app: string;
  newestAt: string;
  callsCount: number;
  amount: bigint;
  modelCounts: Map<string, number>;
  first_at: string;
  last_at: string;
};

function addCallToSession(sessions: Map<string, SessionEntry>, call: Schemas["Call"]) {
  if (!call.session_id) return;
  const currency = call.price.currency;
  const key = `${call.session_id}\0${currency}`;
  const callAmt = BigInt(call.price.amount);
  const prev = sessions.get(key);
  if (!prev) {
    sessions.set(key, {
      session_id: call.session_id,
      currency,
      client_app: call.client_app,
      newestAt: call.at,
      callsCount: 1,
      amount: callAmt,
      modelCounts: new Map([[call.model, 1]]),
      first_at: call.at,
      last_at: call.at,
    });
    return;
  }
  prev.callsCount += 1;
  prev.amount += callAmt;
  prev.modelCounts.set(call.model, (prev.modelCounts.get(call.model) ?? 0) + 1);
  if (call.at > prev.newestAt) {
    prev.newestAt = call.at;
    prev.client_app = call.client_app;
  }
  if (call.at < prev.first_at) prev.first_at = call.at;
  if (call.at > prev.last_at) prev.last_at = call.at;
}

function dominantModel(counts: Map<string, number>) {
  let best = "";
  let bestCount = -1;
  for (const [model, count] of counts) {
    if (count > bestCount || (count === bestCount && model < best)) {
      best = model;
      bestCount = count;
    }
  }
  return best;
}

function compareStringsDesc(a: string, b: string) {
  return a === b ? 0 : b > a ? 1 : -1;
}

function compareStringsAsc(a: string, b: string) {
  return a === b ? 0 : a < b ? -1 : 1;
}

function compareSessionEntries(a: SessionEntry, b: SessionEntry) {
  return (
    compareStringsDesc(a.last_at, b.last_at) ||
    compareStringsAsc(a.session_id, b.session_id) ||
    compareStringsAsc(a.currency, b.currency)
  );
}

type SessionCursor = { s: string; u: string; f: string; o: number };

function encodeSessionCursor(cursor: SessionCursor) {
  return btoa(encodeURIComponent(JSON.stringify(cursor)));
}

function decodeSessionCursor(raw: string | null): SessionCursor | null | undefined {
  if (!raw) return null;
  try {
    return JSON.parse(decodeURIComponent(atob(raw))) as SessionCursor;
  } catch {
    return undefined;
  }
}

function sessionOffset(rawCursor: string | null, since: string, until: string, filterSig: string) {
  const cur = decodeSessionCursor(rawCursor);
  if (cur === undefined) return null;
  if (!cur) return 0;
  const ok = cur.s === since && cur.u === until && cur.f === filterSig && Number.isInteger(cur.o) && cur.o >= 0;
  return ok ? cur.o : null;
}

function nextUntilTick(state: MockState, query: URLSearchParams) {
  if (query.get("until")) return 0;
  const tick = (sessionClocks.get(state) ?? 0) + 1;
  sessionClocks.set(state, tick);
  return tick;
}

function listCallSessions({ query, state }: Ctx<"/api/console/v1/calls/sessions", "get">) {
  const bad = validateCallFilters(query);
  if (bad.length > 0) return failFields(bad);
  const win = resolveBoundedWindow(query, nextUntilTick(state, query));
  if (!win) return failFields([{ pointer: "since", message: "choose a nonempty window of at most 31 days" }]);
  const filterSig = ["model", "client_app", "key_id", "workspace_id", "request_id", "status", "fell_back"]
    .map((k) => `${k}=${query.get(k) ?? ""}`)
    .join("&");
  const offset = sessionOffset(query.get("cursor"), win.since, win.until, filterSig);
  if (offset === null) return fail(400, "invalid_request");
  const sessions = new Map<string, SessionEntry>();
  for (const call of matchingCalls(state.calls, win.bounded)) addCallToSession(sessions, call);
  const ordered = [...sessions.values()].sort(compareSessionEntries).map((g): Schemas["CallSession"] => ({
    session_id: g.session_id,
    client_app: g.client_app,
    calls: String(g.callsCount),
    price: { amount: g.amount.toString(), currency: g.currency },
    main_model: dominantModel(g.modelCounts),
    first_at: g.first_at,
    last_at: g.last_at,
  }));
  const limit = Number(query.get("limit") ?? 50);
  const end = offset + limit;
  const nextCursor =
    end < ordered.length ? encodeSessionCursor({ s: win.since, u: win.until, f: filterSig, o: end }) : null;
  return {
    status: 200,
    json: { data: ordered.slice(offset, end), next_cursor: nextCursor, since: win.since, until: win.until },
  } as const;
}

function aggregateCalls({ query, state }: Ctx<"/api/console/v1/calls/aggregate", "get">) {
  const bad = validateCallFilters(query);
  if (bad.length > 0) return failFields(bad);
  const group = (query.get("group_by") ?? "day") as Schemas["CallAggregates"]["group_by"];
  if (group !== "day" && group !== "model" && group !== "key") return fail(400, "invalid_request");
  const win = resolveBoundedWindow(query);
  if (!win) return failFields([{ pointer: "since", message: "choose a nonempty window of at most 31 days" }]);
  const groups = new Map<string, { bucket: string; calls: number; amount: bigint; currency: string }>();
  for (const call of matchingCalls(state.calls, win.bounded)) {
    const bucket = group === "day" ? call.at.slice(0, 10) : group === "model" ? call.model : String(call.key_id);
    const key = `${bucket}\0${call.price.currency}`;
    const entry = groups.get(key) ?? { bucket, calls: 0, amount: 0n, currency: call.price.currency };
    entry.calls += 1;
    entry.amount += BigInt(call.price.amount);
    groups.set(key, entry);
  }
  const data = [...groups.values()]
    .sort((a, b) => a.bucket.localeCompare(b.bucket) || a.currency.localeCompare(b.currency))
    .map((e) => ({
      bucket: e.bucket,
      calls: String(e.calls),
      price: { amount: e.amount.toString(), currency: e.currency },
    }));
  return { status: 200, json: { data, group_by: group, since: win.since, until: win.until } } as const;
}

function exportCalls({ query, state }: Ctx<"/api/console/v1/calls/export", "get">) {
  const bad = validateCallFilters(query);
  if (bad.length > 0) return failFields(bad);
  const win = resolveBoundedWindow(query);
  if (!win) return failFields([{ pointer: "since", message: "choose a nonempty window of at most 31 days" }]);
  const matched = matchingCalls(state.calls, win.bounded);
  if (matched.length > 10_000) {
    return failFields([{ pointer: "since", message: "more than 10000 calls; narrow the window or filters" }]);
  }
  const cell = (value: string) => (/[",\n]/.test(value) ? `"${value.replaceAll('"', '""')}"` : value);
  const columns = (call: Schemas["Call"]) => [
    call.id,
    call.at,
    call.workspace_id,
    call.key_id,
    call.model,
    call.client_app,
    call.error_kind === "" ? "success" : "error",
    call.error_kind,
    call.price.amount,
    call.price.currency,
  ];
  const lines = [
    "request_id,at,workspace_id,key_id,model,client_app,status,error_kind,price,currency",
    ...matched.map((call) => columns(call).map(cell).join(",")),
  ];
  return {
    status: 200,
    json: undefined,
    text: `${lines.join("\n")}\n`,
    headers: { "Content-Disposition": 'attachment; filename="calls-2026-10-14.csv"' },
  } as const;
}

function getCall({ params, state }: Ctx<"/api/console/v1/calls/{call_id}", "get">) {
  const call = state.calls.find((candidate) => candidate.id === params.call_id);
  return call ? ({ status: 200, json: call } as const) : fail(404, "not_found");
}

export const callHandlers: Handler<MockState>[] = [
  route("get", `${base}/calls`, { ...signedIn, permission: "calls.view" }, listCalls),
  route("get", `${base}/calls/sessions`, { ...signedIn, permission: "calls.view" }, listCallSessions),
  route("get", `${base}/calls/aggregate`, { ...signedIn, permission: "calls.view" }, aggregateCalls),
  route("get", `${base}/calls/export`, { ...signedIn, permission: "calls.view" }, exportCalls),
  route("get", `${base}/calls/{call_id}`, { ...signedIn, permission: "calls.view" }, getCall),
];
