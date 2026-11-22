import { ApiError, consoleApi, type Schemas, unwrap } from "@axiom/api";
import {
  accountKey,
  holdsIdentity,
  useErrors,
  useSessionBound,
  useShowRecoveryCodes,
  useStartSession,
} from "@axiom/app";
import type DesktopSecurity from "@axiom/boards/AxApp/regions/Security";
import { registerPasskey } from "@axiom/login";
import { type InfiniteData, infiniteQueryOptions, queryOptions, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { identitiesQuery, passkeysQuery } from "../queries";

export type SessionsState = { available: boolean; sessions: Schemas["SessionDevice"][] };
export type PasskeyRegistrationAvailability = "ready" | "unsupported" | "site_disabled" | "wrong_domain";
export type InvitationsPage = {
  available: boolean;
  link?: string;
  invitees: Schemas["Invitee"][];
  next_cursor: string | null;
};

export const sessionsQuery = queryOptions({
  queryKey: [accountKey, "sessions"],
  queryFn: async ({ signal }): Promise<SessionsState> => {
    try {
      const data = await unwrap(consoleApi.GET("/api/console/v1/sessions", { signal }));
      if (!data.sessions) return { available: false, sessions: [] };
      return { available: true, sessions: sortSessions(data.sessions) };
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) {
        return { available: false, sessions: [] };
      }
      throw error;
    }
  },
});

// Keep current device at the top so the signed-in user never mistakes it for a revocable peer, then newest first.
export function sortSessions(list: readonly Schemas["SessionDevice"][]): Schemas["SessionDevice"][] {
  return [...list].sort((a, b) => {
    if (Boolean(a.current) !== Boolean(b.current)) return a.current ? -1 : 1;
    const at = Date.parse(a.last_seen_at ?? a.created_at ?? "") || 0;
    const bt = Date.parse(b.last_seen_at ?? b.created_at ?? "") || 0;
    return bt - at;
  });
}

export const invitationsQuery = infiniteQueryOptions({
  queryKey: [accountKey, "invitations"],
  queryFn: async ({ pageParam, signal }): Promise<InvitationsPage> => {
    try {
      const data = await unwrap(
        consoleApi.GET("/api/console/v1/invitations", { params: { query: { cursor: pageParam } }, signal }),
      );
      return {
        available: true,
        link: data.link,
        invitees: data.invitees ?? [],
        next_cursor: data.next_cursor ?? null,
      };
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) {
        return { available: false, invitees: [], next_cursor: null };
      }
      throw error;
    }
  },
  initialPageParam: undefined as string | undefined,
  getNextPageParam: (page) => (page.available ? (page.next_cursor ?? undefined) : undefined),
});

export function flattenInvitees(data: InfiniteData<InvitationsPage>): {
  available: boolean;
  invitees: Schemas["Invitee"][];
  link?: string;
} {
  if (data.pages.length === 0) return { available: false, invitees: [] };
  const first = data.pages[0];
  if (first && !first.available) return { available: false, invitees: [] };
  return {
    available: true,
    invitees: data.pages.flatMap((page) => page.invitees),
    link: first?.link,
  };
}

export function passkeyRegistrationAvailability(
  site: Schemas["Site"] | undefined,
  loc: { origin: string; hostname: string } | undefined,
  supported: boolean,
): PasskeyRegistrationAvailability {
  if (!supported) return "unsupported";
  if (!site?.passkeys) return "site_disabled";
  if (!loc) return "wrong_domain";
  if (site.login_origin) {
    try {
      return new URL(site.login_origin).origin === loc.origin ? "ready" : "wrong_domain";
    } catch {
      return "wrong_domain";
    }
  }
  return site.login_domain && site.login_domain === loc.hostname ? "ready" : "wrong_domain";
}

export function validatePasswordChange(
  current: string,
  next: string,
  confirm: string,
): "currentMissing" | "tooShort" | "mismatch" | "same" | null {
  if (current === "") return "currentMissing";
  if (Array.from(next).length < 12) return "tooShort";
  if (next !== confirm) return "mismatch";
  if (next === current) return "same";
  return null;
}

export function validateTotpReplace(currentCode: string, newCode: string): "currentMissing" | "newInvalid" | null {
  if (currentCode.trim() === "") return "currentMissing";
  if (!/^\d{6}$/.test(newCode.trim())) return "newInvalid";
  return null;
}

type AuthRows = Parameters<typeof DesktopSecurity>[0]["rows"]["authRows0"];
type SessionRows = Parameters<typeof DesktopSecurity>[0]["rows"]["sessions0"];

export type TotpStatus = "off" | "on" | "unknown";

// totpEnabled is the session's totp_enabled, which says it outright; a server older than that field omits it, and
// the status is inferred from mfa_enabled as before.
export function totpStatus(
  totpEnabled: boolean | undefined,
  mfaEnabled: boolean,
  passkeyCount: number | undefined,
): TotpStatus {
  if (totpEnabled !== undefined) return totpEnabled ? "on" : "off";
  if (!mfaEnabled) return "off";
  // A passkey can account for the aggregate flag on its own. Only a loaded, empty list rules that out.
  return passkeyCount === 0 ? "on" : "unknown";
}

export function buildAuthRows(t: TFunction, status: TotpStatus): AuthRows {
  const title = t("security.twoStep.title");
  const rows = [
    {
      key: "password",
      title: t("security.password.title"),
      hint: t("security.password.hint"),
      action: t("security.password.change"),
    },
  ];
  if (status !== "on") {
    rows.push({
      key: "enable",
      title,
      hint: t(status === "off" ? "security.twoStep.off" : "security.twoStep.unknown"),
      action: t("security.twoStep.enable"),
    });
  }
  if (status !== "off") {
    rows.push(
      {
        key: "replace",
        title: status === "on" ? title : t("security.twoStep.authenticator"),
        hint:
          status === "on"
            ? `${t("security.twoStep.on")} · ${t("security.twoStep.body")}`
            : t("security.twoStep.replaceHint"),
        action: t("security.twoStep.replace"),
      },
      {
        key: "recovery",
        title: t("security.twoStep.codesTitle"),
        hint: t("security.twoStep.codesHint"),
        action: t("security.twoStep.regenerate"),
      },
    );
  }
  return rows.map(({ key, title, hint, action }) => ({
    key,
    kind: "0",
    f0: "qrow",
    f1: "qrl",
    f2: title,
    f3: hint,
    f4: "qrc",
    f5: "button",
    f6: key === "enable" && status === "off" ? "btn pri sm " : "btn sec sm ",
    f7: action,
  }));
}

export function buildSessionRows(
  t: TFunction,
  formatDateTime: (date: Date) => string,
  state: { status: "pending" } | { status: "ready"; data: SessionsState },
): SessionRows {
  if (state.status === "pending") {
    return [{ key: "loading", kind: "0", f0: "qses", f1: t("security.loading"), f2: "", f3: "", f4: "" }];
  }
  if (!state.data.available) {
    return [
      { key: "unavailable", kind: "0", f0: "qses", f1: t("security.sessions.unavailable"), f2: "", f3: "", f4: "" },
    ];
  }
  if (state.data.sessions.length === 0) {
    return [{ key: "empty", kind: "0", f0: "qses", f1: t("security.sessions.empty"), f2: "", f3: "", f4: "" }];
  }
  return state.data.sessions.map((device, index) => {
    const network = device.ip_group?.trim() || t("security.sessions.unknownNetwork");
    const lastSeen = device.last_seen_at ? formatDateTime(new Date(device.last_seen_at)) : "-";
    const created = device.created_at ? formatDateTime(new Date(device.created_at)) : "-";
    const title = device.client?.trim() || t("security.sessions.unknownClient");
    const meta = t("security.sessions.meta", { network, lastSeen, created });
    if (device.current) {
      return {
        key: device.id ?? `current-${index}`,
        kind: "0",
        f0: "qses",
        f1: title,
        f2: meta,
        f3: "qtag ok",
        f4: t("security.sessions.currentTag"),
      };
    }
    return {
      key: device.id ?? `session-${index}`,
      kind: "1",
      f0: "qses",
      f1: title,
      f2: meta,
      f3: "button",
      f4: "btn sec sm ",
      f5: t("security.sessions.revoke"),
    };
  });
}

const passkeyUnavailableHint = {
  ready: "security.passkeys.body",
  unsupported: "security.passkeys.unsupported",
  site_disabled: "security.passkeys.disabledBySite",
  wrong_domain: "security.passkeys.wrongDomain",
} as const;

export function buildPasskeyRows(
  t: TFunction,
  formatDateTime: (date: Date) => string,
  passkeys: readonly Schemas["Passkey"][],
  availability: PasskeyRegistrationAvailability,
): SessionRows {
  if (passkeys.length === 0) {
    return [
      {
        key: "empty",
        kind: "0",
        f0: "qses",
        f1: t("security.passkeys.empty"),
        f2: t(passkeyUnavailableHint[availability]),
        f3: "",
        f4: "",
      },
    ];
  }
  const rows: SessionRows = passkeys.map((passkey) => {
    const created = formatDateTime(new Date(passkey.created_at));
    const lastUsed = passkey.last_used_at
      ? t("security.passkeys.lastUsed", { time: formatDateTime(new Date(passkey.last_used_at)) })
      : t("security.passkeys.neverUsed");
    return {
      key: passkey.id,
      kind: "1",
      f0: "qses",
      f1: passkey.name,
      f2: t("security.passkeys.meta", { created, lastUsed }),
      f3: "button",
      f4: "btn sec sm ",
      f5: t("security.passkeys.remove"),
    };
  });
  return availability === "ready"
    ? rows
    : [
        ...rows,
        {
          key: "availability-hint",
          kind: "0",
          f0: "qses",
          f1: t(passkeyUnavailableHint[availability]),
          f2: "",
          f3: "",
          f4: "",
        },
      ];
}

export function buildIdentityRows(
  t: TFunction,
  names: readonly Schemas["OAuthProvider"][],
  linked: Map<Schemas["OAuthProvider"], Schemas["Identity"]>,
): SessionRows {
  return names.map((provider) => {
    const identity = linked.get(provider);
    return {
      key: provider,
      kind: "1",
      f0: "qses",
      f1: t(`security.identities.providers.${provider}`),
      f2: identity
        ? t("security.identities.linkedAs", { name: identity.display_name })
        : t("security.identities.notLinked"),
      f3: "button",
      f4: "btn sec sm ",
      f5: identity ? t("security.identities.unlink") : t("security.identities.link"),
    };
  });
}

export function buildInvitationRows(
  t: TFunction,
  formatDateTime: (date: Date) => string,
  flat: ReturnType<typeof flattenInvitees>,
): SessionRows {
  if (!flat.available) {
    return [
      { key: "unavailable", kind: "0", f0: "qses", f1: t("security.invitations.unavailable"), f2: "", f3: "", f4: "" },
    ];
  }
  if (flat.invitees.length === 0) {
    return [{ key: "empty", kind: "0", f0: "qses", f1: t("security.invitations.empty"), f2: "", f3: "", f4: "" }];
  }
  return flat.invitees.map((item, idx) => ({
    key: `${item.registered_at ?? idx}-${idx}`,
    kind: "0",
    f0: "qses",
    f1: t("security.invitations.invitee"),
    f2: item.registered_at
      ? t("security.invitations.registeredAt", { time: formatDateTime(new Date(item.registered_at)) })
      : "",
    f3: item.available === false ? "qtag" : "qtag ok",
    f4: item.available === false ? t("security.invitations.statusUnavailable") : t("security.invitations.statusOk"),
  }));
}

function usePasskeyChange() {
  const start = useStartSession();
  const show = useShowRecoveryCodes();
  const bound = useSessionBound("identity");
  const client = useQueryClient();
  const [failure, setFailure] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  async function run(change: (signal: AbortSignal) => Promise<{ token: string; codes?: string[] }>) {
    setBusy(true);
    setFailure(null);
    try {
      const answer = await bound(change);
      if (!answer) return false;
      const { token, codes } = answer.value;
      if (codes?.length) show({ csrf_token: token, recovery_codes: codes }, answer.claim.identity);
      await start(token).catch(() => {});
      await client.invalidateQueries({ queryKey: passkeysQuery.queryKey });
      await client.invalidateQueries({ queryKey: sessionsQuery.queryKey });
      return true;
    } catch (error) {
      setFailure(error);
      return false;
    } finally {
      setBusy(false);
    }
  }
  return { failure, busy, run };
}

async function runAddPasskey(passkeyName: string, passkeyPassword: string, signal: AbortSignal) {
  const begin = await unwrap(
    consoleApi.POST("/api/console/v1/auth/passkeys/register/begin", {
      body: { name: passkeyName, password: passkeyPassword },
      signal,
    }),
  );
  const credential = await registerPasskey(begin.public_key);
  const added = await unwrap(
    consoleApi.POST("/api/console/v1/auth/passkeys/register/finish", {
      params: { query: { flow_id: begin.flow_id } },
      body: credential,
      signal,
    }),
  );
  return { token: "", codes: added.recovery_codes };
}

export function usePasskeyActions() {
  const { t } = useTranslation();
  const errors = useErrors();
  const [password, setPassword] = useState("");
  const [localError, setLocalError] = useState<string | null>(null);
  const { failure, busy, run } = usePasskeyChange();
  const supported = typeof window !== "undefined" && "PublicKeyCredential" in window;

  async function add(passkeyName: string, passkeyPassword: string) {
    return run(async (signal) => {
      const res = await runAddPasskey(passkeyName, passkeyPassword, signal);
      setPassword("");
      return res;
    });
  }

  async function remove(id: string) {
    if (password === "") return setLocalError(t("security.passkeys.passwordRequired"));
    setLocalError(null);
    await run(async (signal) => {
      const grant = await unwrap(
        consoleApi.DELETE("/api/console/v1/auth/passkeys/{id}", {
          params: { path: { id } },
          body: { password },
          signal,
        }),
      );
      return { token: grant.csrf_token, codes: grant.recovery_codes };
    });
  }

  return {
    errors,
    password,
    localError,
    failure,
    busy,
    supported,
    add,
    remove,
    setPasswordValue: (v: string) => {
      setPassword(v);
      setLocalError(null);
    },
  };
}

export function useIdentityActions() {
  const errors = useErrors();
  const start = useStartSession();
  const show = useShowRecoveryCodes();
  const bound = useSessionBound();
  const change = useSessionBound("identity");
  const queryClient = useQueryClient();
  const [failure, setFailure] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  async function link(provider: Schemas["OAuthProvider"]) {
    setBusy(true);
    setFailure(null);
    try {
      const answer = await bound((signal) =>
        unwrap(consoleApi.POST("/api/console/v1/auth/oauth/link/start", { body: { provider }, signal })),
      );
      if (!answer) return setBusy(false);
      window.location.assign(answer.value.authorize_url);
    } catch (error) {
      setFailure(error);
      setBusy(false);
    }
  }

  async function unlink(provider: Schemas["OAuthProvider"]) {
    setBusy(true);
    setFailure(null);
    try {
      const answer = await change((signal) =>
        unwrap(
          consoleApi.DELETE("/api/console/v1/auth/identities/{provider}", { params: { path: { provider } }, signal }),
        ),
      );
      if (!answer) return;
      const grant = answer.value;
      if (grant.recovery_codes?.length) {
        show({ csrf_token: grant.csrf_token, recovery_codes: grant.recovery_codes }, answer.claim.identity);
      }
      await start(grant.csrf_token).catch(() => {});
      if (!holdsIdentity(queryClient, answer.claim.identity)) return;
      await queryClient.invalidateQueries({ queryKey: identitiesQuery.queryKey });
      await queryClient.invalidateQueries({ queryKey: sessionsQuery.queryKey });
    } catch (error) {
      setFailure(error);
    } finally {
      setBusy(false);
    }
  }

  return { errors, failure, busy, link, unlink };
}
