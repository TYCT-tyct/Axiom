import type { Page } from "@playwright/test";
import type { paths } from "../packages/api/src/console.gen.ts";
import {
  aKey,
  emptyState,
  goodInvite,
  knownEmail,
  type Level,
  limitedEmail,
  type MockState,
  mfaEmail,
  type Schemas,
  usualState,
  worstState,
} from "./console-data.ts";

// The console API, answered from memory by Playwright's request interception, until the Go handlers exist. Every route
// is typed by api/console.yaml through the generated types: its path and method must exist in the contract, and the
// request body and every reply (status and JSON) must have the contract's shape, or this file stops compiling. It
// also checks what the contract asks of a client: the session, its level, the permission, the CSRF token, the Origin
// and the JSON content type, so a page that forgets one fails its test as it would fail against the server.

type Method = "get" | "post" | "put" | "delete";
type Path = keyof paths;
type Responses<P extends Path, M extends Method> = paths[P] extends Record<M, { responses: infer R }> ? R : never;
type JsonOf<R> = R extends { content: { "application/json": infer J } } ? J : undefined;
type Body<P extends Path, M extends Method> =
  paths[P] extends Record<M, { requestBody: { content: { "application/json": infer B } } }> ? B : undefined;

type Reply<P extends Path, M extends Method> = {
  [K in keyof Responses<P, M> & number]: {
    status: K;
    json: JsonOf<Responses<P, M>[K]>;
    headers?: Record<string, string>;
  };
}[keyof Responses<P, M> & number];

type Needs = { level: Level | "any" | "signed_in"; permission?: string };
type Ctx<P extends Path, M extends Method> = {
  body: Body<P, M>;
  params: Record<string, string>;
  query: URLSearchParams;
  state: MockState;
};
type AnyReply = { status: number; json?: unknown; headers?: Record<string, string> };
type Handler = { method: Method; pattern: RegExp; needs: Needs; handle: (ctx: Ctx<Path, Method>) => AnyReply };

const base = "/api/console/v1";

function route<P extends Path, M extends Method>(
  method: M,
  path: P,
  needs: Needs,
  handle: (ctx: Ctx<P, M>) => Reply<P, M>,
): Handler {
  const pattern = new RegExp(`^${path.replace(/\{(\w+)\}/g, "(?<$1>[^/]+)")}$`);
  return { method, pattern, needs, handle: handle as unknown as Handler["handle"] };
}

const fail = <S extends number>(status: S, code: Schemas["ErrorCode"], extra?: Record<string, string>) => ({
  status,
  json: { error: { code, message: code } } satisfies Schemas["Error"],
  headers: extra,
});

const ok = <J>(json: J) => ({ status: 200 as const, json });

const grant = (state: MockState, status: Schemas["SessionGrant"]["status"]): Schemas["SessionGrant"] => {
  state.csrfCount += 1;
  state.csrf = `csrf-${state.csrfCount}`;
  return { status, csrf_token: state.csrf };
};

const session = (state: MockState): Schemas["Session"] => ({
  user: {
    id: `user_${"0".repeat(26)}` as Schemas["UserId"],
    email: state.user.email,
    invite_code: state.user.inviteCode,
  },
  account: `acct_${"0".repeat(26)}` as Schemas["AccountId"],
  level: state.level === "none" ? "password" : state.level === "pending_mfa" ? "pending_mfa" : state.level,
  mfa_enabled: state.mfaEnabled,
  csrf_token: state.csrf,
  expires_at: "2026-10-15T00:00:00Z",
  permissions: state.permissions,
});

const none: Needs = { level: "none" };
const signedIn: Needs = { level: "signed_in" };
const recoveryCodes = Array.from({ length: 10 }, (_, i) => `abcd-efgh-ijkl-${String(1000 + i)}`);

// Sign-in is decided by the email, so a test picks the case it wants: a plain account, one with two-step verification,
// and one that is rate limited.
function signIn({ body, state }: Ctx<"/api/console/v1/auth/sign-in", "post">) {
  if (body.email === limitedEmail) {
    return fail(429, "rate_limited", { "Retry-After": "30" });
  }
  if (body.password !== state.user.password || ![knownEmail, mfaEmail, state.user.email].includes(body.email)) {
    return fail(401, "invalid_credentials");
  }
  state.mfaEnabled = body.email === mfaEmail;
  state.level = state.mfaEnabled ? "pending_mfa" : "password";
  return { status: 200, json: grant(state, state.mfaEnabled ? "mfa_required" : "signed_in") } as const;
}

function verifyMfa({ body, state }: Ctx<"/api/console/v1/auth/mfa", "post">) {
  const code = body.code.toLowerCase();
  if (code !== state.user.mfaCode && code !== state.user.recoveryCode) {
    return fail(401, "invalid_code");
  }
  state.level = "mfa";
  return { status: 200, json: grant(state, "signed_in") } as const;
}

function register({ body, state }: Ctx<"/api/console/v1/auth/register", "post">) {
  if (body.invite_code !== goodInvite) {
    return fail(422, "invalid_invite");
  }
  if (body.email === knownEmail) {
    return fail(409, "email_taken");
  }
  state.user.email = body.email;
  state.user.password = body.password;
  return { status: 201, json: { user_id: `user_${"1".repeat(26)}` as Schemas["UserId"] } } as const;
}

function confirmTotp({ body, state }: Ctx<"/api/console/v1/auth/totp/confirm", "post">) {
  if (body.password !== state.user.password) {
    return fail(401, "invalid_credentials");
  }
  if (body.code !== state.user.mfaCode) {
    return fail(401, "invalid_code");
  }
  state.mfaEnabled = true;
  state.level = "mfa";
  const { csrf_token } = grant(state, "signed_in");
  return { status: 200, json: { csrf_token, recovery_codes: recoveryCodes } } as const;
}

function regenerate({ body, state }: Ctx<"/api/console/v1/auth/recovery-codes", "post">) {
  if (body.code !== state.user.mfaCode) {
    return fail(401, "invalid_code");
  }
  const { csrf_token } = grant(state, "signed_in");
  return { status: 200, json: { csrf_token, recovery_codes: recoveryCodes.toReversed() } } as const;
}

// A page of a list: the cursor is the offset, tied to the filters it was made for, and a made-up one is a 400.
function paged<T>(items: T[], query: URLSearchParams) {
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

function listCalls({ query, state }: Ctx<"/api/console/v1/calls", "get">) {
  const since = query.get("since");
  const until = query.get("until");
  const matching = state.calls.filter(
    (call) =>
      (!query.get("model") || call.model === query.get("model")) &&
      (!query.get("client_app") || call.client_app === query.get("client_app")) &&
      (!since || call.at >= new Date(since).toISOString()) &&
      (!until || call.at < new Date(until).toISOString()),
  );
  const page = paged(matching, query);
  return page ? ({ status: 200, json: page } as const) : fail(400, "invalid_request");
}

function listKeys({ query, state }: Ctx<"/api/console/v1/keys", "get">) {
  const page = paged(state.keys, query);
  return page ? ({ status: 200, json: page } as const) : fail(400, "invalid_request");
}

function createKey({ body, state }: Ctx<"/api/console/v1/keys", "post">) {
  const limit = body.spend_limit ?? { amount: "5000000000", currency: "USD" };
  const key = aKey(state.keys.length + 10, {
    name: body.name,
    expires_at: body.expires_at,
    spend_limit: limit,
    limit_remaining: limit,
    limit_reset: body.limit_reset ?? "daily",
    models: body.models,
    ip_allow: body.ip_allow,
  });
  state.keys.unshift(key);
  return { status: 201, json: { key, secret: `${key.prefix}${"x".repeat(45)}${key.last4}` } } as const;
}

function updateKey({ body, params, state }: Ctx<"/api/console/v1/keys/{key_id}", "put">) {
  const index = state.keys.findIndex((key) => key.id === params.key_id);
  const current = state.keys[index];
  if (!current) {
    return fail(404, "not_found");
  }
  const { name, disabled, spend_limit, limit_reset, models, ip_allow } = body;
  const changed = { ...current, name, disabled, spend_limit, limit_reset, models, ip_allow };
  state.keys[index] = changed;
  return { status: 200, json: changed } as const;
}

function deleteKey({ params, state }: Ctx<"/api/console/v1/keys/{key_id}", "delete">) {
  const before = state.keys.length;
  state.keys = state.keys.filter((key) => key.id !== params.key_id);
  return state.keys.length < before ? ({ status: 204, json: undefined } as const) : fail(404, "not_found");
}

function getCall({ params, state }: Ctx<"/api/console/v1/calls/{call_id}", "get">) {
  const call = state.calls.find((candidate) => candidate.id === params.call_id);
  return call ? ({ status: 200, json: call } as const) : fail(404, "not_found");
}

const handlers: Handler[] = [
  route("get", `${base}/version`, none, ({ state }) => ok({ version: state.version })),
  route("get", `${base}/site`, none, ({ state }) => ok(state.site)),
  route("post", `${base}/auth/sign-in`, none, signIn),
  route("post", `${base}/auth/mfa`, { level: "pending_mfa" }, verifyMfa),
  route("post", `${base}/auth/register`, none, register),
  route("post", `${base}/auth/sign-out`, { level: "any" }, ({ state }) => {
    state.level = "none";
    return { status: 204 as const, json: undefined };
  }),
  route("get", `${base}/session`, { level: "any" }, ({ state }) => ok(session(state))),
  route("post", `${base}/auth/totp/setup`, signedIn, () =>
    ok({
      secret: "JBSWY3DPEHPK3PXP",
      uri: "otpauth://totp/Axiom:user@example.com?secret=JBSWY3DPEHPK3PXP&issuer=Axiom",
    }),
  ),
  route("post", `${base}/auth/totp/confirm`, signedIn, confirmTotp),
  route("post", `${base}/auth/recovery-codes`, { level: "mfa" }, regenerate),
  route("get", `${base}/keys`, { ...signedIn, permission: "api_keys.view" }, listKeys),
  route("post", `${base}/keys`, { ...signedIn, permission: "api_keys.manage" }, createKey),
  route("put", `${base}/keys/{key_id}`, { ...signedIn, permission: "api_keys.manage" }, updateKey),
  route("delete", `${base}/keys/{key_id}`, { ...signedIn, permission: "api_keys.manage" }, deleteKey),
  route("get", `${base}/balance`, { ...signedIn, permission: "balance.view" }, ({ state }) => ok(state.balance)),
  route("get", `${base}/calls`, { ...signedIn, permission: "calls.view" }, listCalls),
  route("get", `${base}/calls/{call_id}`, { ...signedIn, permission: "calls.view" }, getCall),
];

// What each level of session has proved, and the least each kind of operation needs.
const proved: Record<Level, number> = { none: 0, pending_mfa: 1, password: 2, mfa: 3 };
const needed: Record<Needs["level"], number> = { none: 0, any: 1, pending_mfa: 1, signed_in: 2, password: 2, mfa: 3 };

function sessionRefusal(state: MockState, needs: Needs) {
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
function refusal(state: MockState, needs: Needs, method: Method, headers: Record<string, string>, origin: string) {
  const refused = sessionRefusal(state, needs);
  if (refused || method === "get") {
    return refused;
  }
  const csrfOk = needs.level === "none" || headers["x-csrf-token"] === state.csrf;
  const jsonOk = headers["content-type"]?.startsWith("application/json");
  return headers.origin === origin && jsonOk && csrfOk ? undefined : fail(403, "origin_rejected");
}

export type MockData = "usual" | "empty" | "worst";

// mockConsole answers the console's API for a page. The returned state is live: a test reads what the page did to it
// (state.log, state.keys) and sets up the account it needs before navigating (state.level = "mfa" is signed in).
export async function mockConsole(page: Page, data: MockData = "usual", over: Partial<MockState> = {}) {
  const state = { usual: usualState, empty: emptyState, worst: worstState }[data]();
  Object.assign(state, over);
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
    const refused = refusal(state, handler.needs, method, headers, url.origin);
    const reply =
      refused ??
      handler.handle({
        body: request.postDataJSON() ?? undefined,
        params: handler.pattern.exec(url.pathname)?.groups ?? {},
        query: url.searchParams,
        state,
      });
    return route.fulfill({
      status: reply.status,
      json: reply.json,
      headers: { "Cache-Control": "no-store", ...reply.headers },
    });
  });
  return state;
}

export const signedInAs = (state: MockState, level: "password" | "mfa" = "password") => {
  state.level = level;
  state.csrf = "csrf-0";
};
