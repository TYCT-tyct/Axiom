import type { Page } from "@playwright/test";
import type { paths } from "../packages/api/src/console.gen.ts";
import { aKey, emptyState, goodInvite, type MockState, type Schemas, usualState, worstState } from "./console-data.ts";
import { type Ctx as CoreCtx, createRoute, fail, type Handler, type MockData, mockApi, ok, paged } from "./mock.ts";
import { confirmTotp, knownEmail, regenerate, session, signIn, signOut, totpSetup, verifyMfa } from "./mock-auth.ts";

// The console API (api/console.yaml), answered from memory (mock.ts) with the data of console-data.ts.

const base = "/api/console/v1";
const route = createRoute<paths, MockState>();
type Ctx<K extends keyof paths, M extends "get" | "post" | "put" | "delete"> = CoreCtx<paths, K, M, MockState>;
const none = { level: "none" } as const;
const signedIn = { level: "signed_in" } as const;

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

function listStatement({ query, state }: Ctx<"/api/console/v1/statement", "get">) {
  const page = paged(state.statement, query);
  return page ? ({ status: 200, json: page } as const) : fail(400, "invalid_request");
}

// The export is a file, not JSON: the newest rows of the same statement under a header, as the server writes them.
function exportStatement({ state }: Ctx<"/api/console/v1/statement.csv", "get">) {
  const columns = (row: Schemas["StatementRow"]) => [
    row.at,
    row.kind,
    row.amount.amount,
    row.balance_after.amount,
    row.amount.currency,
    row.reference,
    row.frozen_change.amount,
    row.frozen_after.amount,
  ];
  const lines = [
    `at,kind,amount,balance_after,currency,reference,frozen_change,frozen_after`,
    ...state.statement.map((row) => columns(row).join(",")),
  ];
  return {
    status: 200,
    json: undefined,
    text: `${lines.join("\n")}\n`,
    headers: { "Content-Disposition": `attachment; filename="statement-2026-10-14.csv"` },
  } as const;
}

const handlers: Handler<MockState>[] = [
  route("get", `${base}/version`, none, ({ state }) => ok({ version: state.version })),
  route("get", `${base}/site`, none, ({ state }) => ok(state.site)),
  route("post", `${base}/auth/sign-in`, none, signIn),
  route("post", `${base}/auth/mfa`, { level: "pending_mfa" }, verifyMfa),
  route("post", `${base}/auth/register`, none, register),
  route("post", `${base}/auth/sign-out`, { level: "any" }, signOut),
  route("get", `${base}/session`, { level: "any" }, ({ state }) => ok(session(state))),
  route("post", `${base}/auth/totp/setup`, signedIn, totpSetup),
  route("post", `${base}/auth/totp/confirm`, signedIn, confirmTotp),
  route("post", `${base}/auth/recovery-codes`, { level: "mfa" }, regenerate),
  route("get", `${base}/keys`, { ...signedIn, permission: "api_keys.view" }, listKeys),
  route("post", `${base}/keys`, { ...signedIn, permission: "api_keys.manage" }, createKey),
  route("put", `${base}/keys/{key_id}`, { ...signedIn, permission: "api_keys.manage" }, updateKey),
  route("delete", `${base}/keys/{key_id}`, { ...signedIn, permission: "api_keys.manage" }, deleteKey),
  route("get", `${base}/balance`, { ...signedIn, permission: "balance.view" }, ({ state }) => ok(state.balance)),
  route("get", `${base}/calls`, { ...signedIn, permission: "calls.view" }, listCalls),
  route("get", `${base}/calls/{call_id}`, { ...signedIn, permission: "calls.view" }, getCall),
  route("get", `${base}/statement`, { ...signedIn, permission: "balance.view" }, listStatement),
  route("get", `${base}/statement.csv`, { ...signedIn, permission: "balance.view" }, exportStatement),
];

export type { MockData };

// mockConsole answers the console's API for a page. The returned state is live: a test reads what the page did to it
// (state.log, state.keys) and sets up the account it needs before navigating (state.level = "mfa" is signed in).
export async function mockConsole(page: Page, data: MockData = "usual", over: Partial<MockState> = {}) {
  const state = { usual: usualState, empty: emptyState, worst: worstState }[data]();
  Object.assign(state, over);
  return mockApi(page, base, handlers, state);
}

export { signedInAs } from "./mock-auth.ts";

// The server answering the same call, once, with a failure.
export const failOnce = (page: Page, url: string) => {
  let failed = false;
  return page.route(url, (route) => {
    if (failed) {
      return route.fallback();
    }
    failed = true;
    return route.fulfill({ status: 500, json: { error: { code: "internal", message: "internal" } } });
  });
};
