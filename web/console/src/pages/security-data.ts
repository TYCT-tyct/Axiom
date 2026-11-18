import { ApiError, consoleApi, type Schemas, unwrap } from "@axiom/api";
import { accountKey, useErrors, useSessionBound, useShowRecoveryCodes, useStartSession } from "@axiom/app";
import type DesktopSecurity from "@axiom/boards/AxApp/regions/Security";
import { type InfiniteData, infiniteQueryOptions, queryOptions, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { identitiesQuery, passkeysQuery } from "../queries";
import { registerPasskey } from "../webauthn";

export type SessionsState = { available: boolean; sessions: Schemas["SessionDevice"][] };
export type InvitationsPage = {
  available: boolean;
  code?: string;
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
        code: data.code,
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

export function flattenInvitees(data: InfiniteData<InvitationsPage> | undefined): {
  available: boolean;
  invitees: Schemas["Invitee"][];
  link?: string;
} {
  if (!data || data.pages.length === 0) return { available: true, invitees: [] };
  const first = data.pages[0];
  if (first && !first.available) return { available: false, invitees: [] };
  return {
    available: true,
    invitees: data.pages.flatMap((page) => page.invitees),
    link: first?.link,
  };
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

export function buildAuthRows(t: TFunction, mfaEnabled: boolean): AuthRows {
  const rows: Parameters<typeof DesktopSecurity>[0]["rows"]["authRows0"][number][] = [
    {
      key: "password",
      kind: "0",
      f0: "qrow",
      f1: "qrl",
      f2: t("security.password.title"),
      f3: t("security.password.hint"),
      f4: "qrc",
      f5: "button",
      f6: "btn sec sm ",
      f7: t("security.password.change"),
    },
    {
      key: "totp",
      kind: "0",
      f0: "qrow",
      f1: "qrl",
      f2: t("security.twoStep.title"),
      f3: mfaEnabled ? `${t("security.twoStep.on")} · ${t("security.twoStep.body")}` : t("security.twoStep.off"),
      f4: "qrc",
      f5: "button",
      f6: mfaEnabled ? "btn sec sm " : "btn pri sm ",
      f7: mfaEnabled ? t("security.twoStep.replace") : t("security.twoStep.enable"),
    },
  ];
  if (mfaEnabled) {
    rows.push({
      key: "recovery",
      kind: "0",
      f0: "qrow",
      f1: "qrl",
      f2: t("security.twoStep.codesTitle"),
      f3: t("security.twoStep.codesHint"),
      f4: "qrc",
      f5: "button",
      f6: "btn sec sm ",
      f7: t("security.twoStep.regenerate"),
    });
  }
  return rows;
}

export function buildSessionRows(
  t: TFunction,
  formatDateTime: (date: Date) => string,
  state: SessionsState,
): SessionRows {
  if (!state.available) {
    return [{ key: "empty", kind: "0", f0: "qses", f1: t("security.sessions.empty"), f2: "", f3: "", f4: "" }];
  }
  if (state.sessions.length === 0) {
    return [{ key: "empty", kind: "0", f0: "qses", f1: t("security.sessions.empty"), f2: "", f3: "", f4: "" }];
  }
  return state.sessions.map((device, index) => {
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

export function buildPasskeyRows(
  t: TFunction,
  formatDateTime: (date: Date) => string,
  passkeys: readonly Schemas["Passkey"][],
  supported: boolean,
): SessionRows {
  if (passkeys.length === 0) {
    return [
      {
        key: "empty",
        kind: "0",
        f0: "qses",
        f1: t("security.passkeys.empty"),
        f2: supported ? t("security.passkeys.body") : t("security.passkeys.unsupported"),
        f3: "",
        f4: "",
      },
    ];
  }
  return passkeys.map((passkey) => {
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
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [localError, setLocalError] = useState<string | null>(null);
  const { failure, busy, run } = usePasskeyChange();
  const supported = typeof window !== "undefined" && "PublicKeyCredential" in window;

  async function add(passkeyName: string, passkeyPassword: string) {
    return run(async (signal) => {
      const res = await runAddPasskey(passkeyName, passkeyPassword, signal);
      setName("");
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
    name,
    password,
    localError,
    failure,
    busy,
    supported,
    add,
    remove,
    setName,
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
      await queryClient.invalidateQueries({ queryKey: identitiesQuery.queryKey });
    } catch (error) {
      setFailure(error);
    } finally {
      setBusy(false);
    }
  }

  return { errors, failure, busy, link, unlink, clearFailure: () => setFailure(null) };
}
