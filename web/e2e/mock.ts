import type { Page } from "@playwright/test";
import type { components } from "../packages/api/src/console.gen.ts";

// The machinery that answers an app's API from memory by Playwright's request interception, until the Go handlers exist.
// It is the same for the console and the admin: a surface gives its contract's types and the handlers of its routes
// (console-api.ts, admin-api.ts). Every route is typed by the contract through the generated types: its path and method
// must exist in it, and the request body and every reply (status and JSON) must have its shape, or the file stops
// compiling. The server checks what the contract asks of a client before it looks at the request: the session, its level,
// the permission, the CSRF token, the Origin and the JSON content type; and, for a change on the admin API, the
// Idempotency-Key (a repeat gets the stored result, flagged) and the reason. A page that forgets one fails its test as it
// would fail against the server.

type Schemas = components["schemas"];
export type ErrorCode = Schemas["ErrorCode"];
type Level = "none" | "pending_mfa" | "password" | "mfa";

type Stored = { operation: string; status: number; json: unknown };

// What every surface keeps about the person at the keyboard.
export type AuthState = {
  level: Level;
  csrf: string;
  csrfCount: number;
  user: { email: string; password: string; inviteCode: string; mfaCode: string; recoveryCode: string };
  mfaEnabled: boolean;
  permissions: string[];
  version: string;
  // every request the page made, as "METHOD /path", for tests that count them
  log: string[];
  // the replies to changes, by Idempotency-Key
  stored: Map<string, Stored>;
  // how many of the next replies are lost on the way after the server did what was asked: the connection breaks
  dropReplies: number;
};

type Method = "get" | "post" | "put" | "delete";
type Responses<P, K extends keyof P, M extends Method> = P[K] extends Record<M, { responses: infer R }> ? R : never;
type JsonOf<R> = R extends { content: { "application/json": infer J } } ? J : undefined;
type Body<P, K extends keyof P, M extends Method> =
  P[K] extends Record<M, { requestBody: { content: { "application/json": infer B } } }> ? B : undefined;

type Reply<P, K extends keyof P, M extends Method> = {
  [S in keyof Responses<P, K, M> & number]: {
    status: S;
    json: JsonOf<Responses<P, K, M>[S]>;
    headers?: Record<string, string>;
  };
}[keyof Responses<P, K, M> & number];

export type Needs = {
  level: Level | "any" | "signed_in";
  permission?: string;
  // a change on the admin API: needs an Idempotency-Key and a reason
  change?: boolean;
};

export type Ctx<P, K extends keyof P, M extends Method, S> = {
  body: Body<P, K, M>;
  params: Record<string, string>;
  query: URLSearchParams;
  state: S;
};

type AnyReply = { status: number; json?: unknown; headers?: Record<string, string> };
export type Handler<S> = {
  method: Method;
  pattern: RegExp;
  needs: Needs;
  handle: (ctx: Ctx<Record<string, never>, string, Method, S>) => AnyReply;
};

// route declares one answer of a surface: its path (as in the contract) must exist there with this method.
export function createRoute<P extends object, S>() {
  return function route<K extends keyof P & string, M extends Method>(
    method: M,
    path: K,
    needs: Needs,
    handle: (ctx: Ctx<P, K, M, S>) => Reply<P, K, M>,
  ): Handler<S> {
    const pattern = new RegExp(`^${path.replace(/\{(\w+)\}/g, "(?<$1>[^/]+)")}$`);
    return { method, pattern, needs, handle: handle as unknown as Handler<S>["handle"] };
  };
}

export const fail = <C extends number>(status: C, code: ErrorCode, extra?: Record<string, string>) => ({
  status,
  json: { error: { code, message: code } } satisfies Schemas["Error"],
  headers: extra,
});

export const ok = <J>(json: J) => ({ status: 200 as const, json });

// A page of a list: the cursor is the offset, tied to the filters it was made for, and a made-up one is a 400.
export function paged<T>(items: T[], query: URLSearchParams) {
  const filters = [...query].filter(([name]) => name !== "cursor" && name !== "limit").join("&");
  const cursor = query.get("cursor");
  const [stamp, offset] = cursor ? cursor.split(":") : [filters, "0"];
  const start = Number(offset);
  if (stamp !== filters || !Number.isInteger(start)) {
    return undefined;
  }
  const limit = Number(query.get("limit") ?? 50);
  const end = start + limit;
  return { data: items.slice(start, end), next_cursor: end < items.length ? `${filters}:${end}` : null };
}

// What each level of session has proved, and the least each kind of operation needs.
const proved: Record<Level, number> = { none: 0, pending_mfa: 1, password: 2, mfa: 3 };
const needed: Record<Needs["level"], number> = { none: 0, any: 1, pending_mfa: 1, signed_in: 2, password: 2, mfa: 3 };

function sessionRefusal(state: AuthState, needs: Needs) {
  const wrongStage = needs.level === "pending_mfa" && state.level !== "pending_mfa";
  if (needs.level !== "none" && (state.level === "none" || wrongStage)) {
    return fail(401, "unauthenticated");
  }
  if (proved[state.level] < needed[needs.level]) {
    return fail(403, "mfa_required");
  }
  return needs.permission && !state.permissions.includes(needs.permission) ? fail(403, "forbidden") : undefined;
}

// What the server refuses before it looks at the request: no session, a session too weak for the operation, a missing
// permission, and, for a request that changes data, a wrong Origin, content type or CSRF token.
function refusal(state: AuthState, needs: Needs, method: Method, headers: Record<string, string>, origin: string) {
  const refused = sessionRefusal(state, needs);
  if (refused || method === "get") {
    return refused;
  }
  const csrfOk = needs.level === "none" || headers["x-csrf-token"] === state.csrf;
  const jsonOk = headers["content-type"]?.startsWith("application/json");
  return headers.origin === origin && jsonOk && csrfOk ? undefined : fail(403, "origin_rejected");
}

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

// A change on the admin API carries an Idempotency-Key and a reason of 1 to 1000 characters. The reply to a key already
// seen is the stored one, flagged; the same key for another operation is refused.
function changeRefusal(state: AuthState, headers: Record<string, string>, body: unknown, operation: string) {
  const key = headers["idempotency-key"] ?? "";
  const reason = (body as { reason?: unknown } | undefined)?.reason;
  if (!uuid.test(key) || typeof reason !== "string" || reason.length < 1 || reason.length > 1000) {
    return { refused: fail(400, "invalid_request"), key };
  }
  const seen = state.stored.get(key);
  if (!seen) {
    return { key };
  }
  if (seen.operation !== operation) {
    return { refused: fail(409, "idempotency_key_reused"), key };
  }
  return { refused: { status: seen.status, json: seen.json, headers: { "Idempotency-Replayed": "true" } }, key };
}

export type MockData = "usual" | "empty" | "worst";

// mockApi answers a surface's API for a page. The state is live: a test reads what the page did to it (state.log and
// whatever the surface keeps) and sets up what it needs before navigating.
export async function mockApi<S extends AuthState>(page: Page, base: string, handlers: Handler<S>[], state: S) {
  await page.route(`**${base}/**`, async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const method = request.method().toLowerCase() as Method;
    const handler = handlers.find((h) => h.method === method && h.pattern.test(url.pathname));
    state.log.push(`${method.toUpperCase()} ${url.pathname.slice(base.length)}`);
    if (!handler) {
      return route.fulfill({ status: 404, json: fail(404, "not_found").json });
    }
    const headers = await request.allHeaders();
    const body = request.postDataJSON() ?? undefined;
    const operation = `${method} ${url.pathname}`;
    let refused = refusal(state, handler.needs, method, headers, url.origin) as AnyReply | undefined;
    const change = refused || !handler.needs.change ? undefined : changeRefusal(state, headers, body, operation);
    refused ??= change?.refused;
    const reply: AnyReply =
      refused ??
      handler.handle({
        body,
        params: handler.pattern.exec(url.pathname)?.groups ?? {},
        query: url.searchParams,
        state,
      });
    if (change && !refused && reply.status < 400) {
      state.stored.set(change.key, { operation, status: reply.status, json: reply.json });
    }
    if (state.dropReplies > 0) {
      state.dropReplies -= 1;
      return route.abort("connectionreset");
    }
    return route.fulfill({
      status: reply.status,
      json: reply.json,
      headers: { "Cache-Control": "no-store", ...reply.headers },
    });
  });
  return state;
}
