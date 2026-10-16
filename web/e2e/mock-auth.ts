import type { components } from "../packages/api/src/console.gen.ts";
import { type AuthState, fail } from "./mock.ts";

// The answers to the Session tag, which both surfaces offer under their own path with the same shapes. A person signs in
// as a user chosen by the email typed: a plain one, one with two-step verification, and one that is rate limited.

type Schemas = components["schemas"];

export const knownEmail = "user@example.com";
export const knownPassword = "correct horse battery";
export const mfaEmail = "mfa@example.com";
export const limitedEmail = "limited@example.com";

export const userId = (n: number) => `user_${String(n).repeat(26)}` as Schemas["UserId"];
export const accountId = (n: number) => `acct_${String(n).repeat(26)}` as Schemas["AccountId"];

export function authState(permissions: string[]): AuthState {
  return {
    level: "none",
    csrf: "",
    csrfCount: 0,
    user: {
      email: knownEmail,
      password: knownPassword,
      inviteCode: "AXIOM-7K2Q",
      mfaCode: "123456",
      recoveryCode: "aaaa-bbbb-cccc-dddd",
    },
    mfaEnabled: false,
    permissions,
    version: process.env.AXIOM_VERSION ?? "",
    log: [],
    stored: new Map(),
    dropReplies: 0,
  };
}

function grant(state: AuthState, status: Schemas["SessionGrant"]["status"]): Schemas["SessionGrant"] {
  state.csrfCount += 1;
  state.csrf = `csrf-${state.csrfCount}`;
  return { status, csrf_token: state.csrf };
}

export const session = (state: AuthState): Schemas["Session"] => ({
  user: { id: userId(0), email: state.user.email, invite_code: state.user.inviteCode },
  account: accountId(0),
  level: state.level === "none" ? "password" : state.level,
  mfa_enabled: state.mfaEnabled,
  csrf_token: state.csrf,
  expires_at: "2026-10-15T00:00:00Z",
  permissions: state.permissions,
});

const recoveryCodes = Array.from({ length: 10 }, (_, i) => `abcd-efgh-ijkl-${String(1000 + i)}`);

type WithState<B> = { body: B; state: AuthState };

// Sign-in is decided by the email: a known user, one with two-step verification (which waits for its second step), one
// that is rate limited; any other email, or a wrong password, is refused alike.
export function signIn({ body, state }: WithState<Schemas["SignInRequest"]>) {
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

export function verifyMfa({ body, state }: WithState<Schemas["MfaRequest"]>) {
  const code = body.code.toLowerCase();
  if (code !== state.user.mfaCode && code !== state.user.recoveryCode) {
    return fail(401, "invalid_code");
  }
  state.level = "mfa";
  return { status: 200, json: grant(state, "signed_in") } as const;
}

export function signOut({ state }: { state: AuthState }) {
  state.level = "none";
  return { status: 204 as const, json: undefined };
}

export const totpSetup = () => ({
  status: 200 as const,
  json: {
    secret: "JBSWY3DPEHPK3PXP",
    uri: "otpauth://totp/Axiom:user@example.com?secret=JBSWY3DPEHPK3PXP&issuer=Axiom",
  },
});

export function confirmTotp({ body, state }: WithState<Schemas["TotpConfirmRequest"]>) {
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

export function regenerate({ body, state }: WithState<Schemas["RecoveryCodesRequest"]>) {
  if (body.code !== state.user.mfaCode) {
    return fail(401, "invalid_code");
  }
  const { csrf_token } = grant(state, "signed_in");
  return { status: 200, json: { csrf_token, recovery_codes: recoveryCodes.toReversed() } } as const;
}

export const signedInAs = (state: AuthState, level: "password" | "mfa" = "password") => {
  state.level = level;
  state.csrf = "csrf-0";
};
