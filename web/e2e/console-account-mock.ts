import type { components, paths } from "../packages/api/src/console.gen.ts";
import { type AuthState, type Ctx as CoreCtx, createRoute, fail, type Handler, ok, paged } from "./mock.ts";
import { grant, recoveryCodes } from "./mock-auth.ts";

type Schemas = components["schemas"];

export type AccountMockFields = {
  displayName: string;
  pendingEmail?: string;
  emailConfirmToken?: string;
  notifications: Schemas["NotificationPreferences"];
  sessions: Schemas["SessionDevice"][];
  invitees: Schemas["Invitee"][];
  identities: Schemas["Identity"][];
};

type AccountState = AuthState & AccountMockFields;
type Ctx<K extends keyof paths, M extends "get" | "post" | "put" | "delete"> = CoreCtx<paths, K, M, AccountState>;

const base = "/api/console/v1";
const route = createRoute<paths, AccountState>();
const signedIn = { level: "signed_in" } as const;

export function usualAccountFields(): AccountMockFields {
  return {
    displayName: "林知远",
    notifications: {
      low_balance: true,
      top_up: true,
      factor_changed: true,
      spend_anomaly: true,
      threshold: "5000000000",
      webhook_url: "",
      language: "zh",
    },
    sessions: [
      {
        id: "ses_1",
        current: true,
        client: "Chrome on macOS",
        ip_group: "203.0.113.1",
        created_at: "2026-10-20T08:00:00Z",
        last_seen_at: "2026-10-24T09:50:00Z",
      },
      {
        id: "ses_2",
        current: false,
        client: "Safari on iPhone",
        ip_group: "198.51.100.12",
        created_at: "2026-10-22T10:00:00Z",
        last_seen_at: "2026-10-24T08:10:00Z",
      },
      {
        id: "ses_3",
        current: false,
        client: "Firefox on Windows",
        ip_group: "192.0.2.44",
        created_at: "2026-10-19T12:00:00Z",
        last_seen_at: "2026-10-23T18:00:00Z",
      },
    ],
    invitees: [
      { registered_at: "2026-10-22T09:30:00Z", available: true },
      { registered_at: "2026-10-18T14:20:00Z", available: false },
    ],
    identities: [],
  };
}

export function emptyAccountFields(): AccountMockFields {
  const baseFields = usualAccountFields();
  return {
    ...baseFields,
    displayName: "",
    sessions: baseFields.sessions.filter((item) => item.current),
    invitees: [],
  };
}

export function worstAccountFields(long: (text: string, len: number) => string): AccountMockFields {
  const baseFields = usualAccountFields();
  return {
    ...baseFields,
    displayName: long("Lin Zhiyuan ", 100),
    sessions: [
      ...baseFields.sessions,
      ...Array.from({ length: 8 }, (_, n) => ({
        id: `ses_extra_${n}`,
        current: false,
        client: long(`Client ${n} `, 64),
        ip_group: `203.0.113.${n + 10}`,
        created_at: "2026-10-15T08:00:00Z",
        last_seen_at: "2026-10-23T08:00:00Z",
      })),
    ],
    invitees: Array.from({ length: 60 }, (_, n) => ({
      registered_at: new Date(Date.UTC(2026, 9, 24, 9, 0) - n * 3_600_000).toISOString(),
      available: n % 4 !== 0,
    })),
  };
}

function changeDisplayName({ body, state }: Ctx<"/api/console/v1/profile/name", "post">) {
  const trimmed = body.name.trim();
  const len = Array.from(trimmed).length;
  if (len < 1 || len > 100) return fail(400, "invalid_request");
  state.displayName = trimmed;
  return { status: 204 as const, json: undefined };
}

function requestEmailChange({ body, state }: Ctx<"/api/console/v1/profile/email", "post">) {
  if (state.mfaEnabled && state.level !== "mfa") return fail(403, "mfa_required");
  if (body.current_password !== state.user.password) return fail(401, "invalid_credentials");
  if (body.email === "taken@example.com") return fail(409, "email_taken");
  state.pendingEmail = body.email.trim();
  state.emailConfirmToken = "tok-email-1";
  return { status: 202 as const, json: undefined };
}

function confirmEmailChange({ body, state }: Ctx<"/api/console/v1/profile/email/confirm", "post">) {
  if (!body.token || body.token !== state.emailConfirmToken || !state.pendingEmail) {
    return fail(422, "invalid_request");
  }
  state.user.email = state.pendingEmail;
  state.pendingEmail = undefined;
  state.emailConfirmToken = undefined;
  state.sessions = state.sessions.filter((item) => item.current);
  const { csrf_token } = grant(state, "signed_in");
  return ok({ csrf_token, ...(state.mfaEnabled ? { recovery_codes: recoveryCodes } : {}) });
}

function changePassword({ body, state }: Ctx<"/api/console/v1/password", "post">) {
  if (state.mfaEnabled && state.level !== "mfa") return fail(403, "mfa_required");
  if (body.current_password !== state.user.password) return fail(401, "invalid_credentials");
  if (body.new_password === state.user.password || Array.from(body.new_password).length < 12) {
    return fail(422, "invalid_request");
  }
  state.user.password = body.new_password;
  state.sessions = state.sessions.filter((item) => item.current);
  const { csrf_token } = grant(state, "signed_in");
  return ok({ csrf_token, ...(state.mfaEnabled ? { recovery_codes: recoveryCodes } : {}) });
}

function replaceTotp({ body, state }: Ctx<"/api/console/v1/auth/totp/replace", "post">) {
  if (!(state.totpEnabled ?? state.mfaEnabled)) return fail(409, "conflict");
  const current = body.current_code.trim().toLowerCase();
  if ((current !== state.user.mfaCode && current !== state.user.recoveryCode) || body.code !== state.user.mfaCode) {
    return fail(401, "invalid_code");
  }
  state.sessions = state.sessions.filter((item) => item.current);
  const { csrf_token } = grant(state, "signed_in");
  return ok({ csrf_token, recovery_codes: recoveryCodes.toReversed() });
}

function unlinkIdentity({ params, state }: Ctx<"/api/console/v1/auth/identities/{provider}", "delete">) {
  const before = state.identities.length;
  state.identities = state.identities.filter((item) => item.provider !== params.provider);
  if (state.identities.length === before) return fail(404, "not_found");
  state.sessions = state.sessions.filter((item) => item.current);
  const { csrf_token } = grant(state, "signed_in");
  return ok({
    csrf_token,
    ...((state.totpEnabled ?? state.mfaEnabled) ? { recovery_codes: recoveryCodes } : {}),
  });
}

function revokeAllSessions({ state }: Ctx<"/api/console/v1/sessions", "delete">) {
  const before = state.sessions.length;
  state.sessions = state.sessions.filter((item) => item.current);
  return ok({ revoked: before - state.sessions.length });
}

function revokeOneSession({ params, state }: Ctx<"/api/console/v1/sessions/{session_id}", "delete">) {
  const found = state.sessions.find((item) => item.id === params.session_id);
  if (!found || found.current) return fail(404, "not_found");
  state.sessions = state.sessions.filter((item) => item.id !== params.session_id);
  return { status: 204 as const, json: undefined };
}

function listInvitations({ query, state }: Ctx<"/api/console/v1/invitations", "get">) {
  const page = paged(state.invitees, query);
  if (!page) return fail(400, "invalid_request");
  return ok({
    code: state.user.inviteCode,
    link: `https://example.com/console/register?invite=${state.user.inviteCode}`,
    invitees: page.data,
    next_cursor: page.next_cursor,
  });
}

export function accountHandlers<S extends AccountState>(): Handler<S>[] {
  return [
    route("post", `${base}/auth/totp/replace`, { level: "mfa" }, replaceTotp),
    route("get", `${base}/auth/identities`, signedIn, ({ state }) => ok({ identities: state.identities })),
    route("delete", `${base}/auth/identities/{provider}`, signedIn, unlinkIdentity),
    route("post", `${base}/password`, signedIn, changePassword),
    route("get", `${base}/profile/name`, signedIn, ({ state }) => ok({ name: state.displayName })),
    route("post", `${base}/profile/name`, signedIn, changeDisplayName),
    route("post", `${base}/profile/email`, signedIn, requestEmailChange),
    route("post", `${base}/profile/email/confirm`, signedIn, confirmEmailChange),
    route("get", `${base}/sessions`, signedIn, ({ state }) => ok({ sessions: state.sessions })),
    route("delete", `${base}/sessions`, signedIn, revokeAllSessions),
    route("delete", `${base}/sessions/{session_id}`, signedIn, revokeOneSession),
    route("get", `${base}/invitations`, signedIn, listInvitations),
  ] as unknown as Handler<S>[];
}
