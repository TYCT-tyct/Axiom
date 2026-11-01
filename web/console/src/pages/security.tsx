import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import {
  RecoveryDialog,
  TotpSetupDialog,
  useBeginTotp,
  useErrors,
  useSession,
  useSessionBound,
  useShowRecoveryCodes,
  useStartSession,
} from "@axiom/app";
import {
  Badge,
  Button,
  CopyButton,
  DataState,
  FormError,
  Icon,
  PageHeader,
  Panel,
  TextField,
  useFormat,
} from "@axiom/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type ChangeEvent, useState } from "react";
import { useTranslation } from "react-i18next";
import { usePendingCodes } from "../pending-codes";
import { identitiesQuery, passkeysQuery, siteQuery } from "../queries";
import { registerPasskey } from "../webauthn";

export default function Security() {
  const { t } = useTranslation();
  const errors = useErrors();
  const session = useSession();
  const oauth = new URLSearchParams(window.location.search).get("oauth");
  const pending = usePendingCodes();
  const [regenerate, setRegenerate] = useState(false);
  return (
    <>
      <PageHeader title={t("nav.security")} description={t("security.description")} />
      <div className="space-y-4 p-4 sm:p-6">
        {oauth === "identity_linked" || oauth === "identity_already_linked" ? (
          <FormError>{t(`security.oauth.${oauth}`)}</FormError>
        ) : null}
        {pending?.failure ? (
          <FormError>
            <p>{t("security.pendingCodes.failed")}</p>
            <p className="text-t2">{errors.message(pending.failure)}</p>
            <div className="flex flex-wrap gap-2 pt-1">
              <Button size="sm" onClick={pending.retry} disabled={pending.pending}>
                {t("security.pendingCodes.retry")}
              </Button>
              {session.mfa_enabled ? (
                <Button size="sm" onClick={() => setRegenerate(true)}>
                  {t("security.pendingCodes.regenerate")}
                </Button>
              ) : null}
            </div>
          </FormError>
        ) : null}
        <TwoStep />
        <Passkeys />
        <LinkedAccounts />
        <Invite />
      </div>
      {regenerate ? <RecoveryDialog onClose={() => setRegenerate(false)} /> : null}
    </>
  );
}

// Starting to enable asks for a new secret and opens the dialog that proves it; nothing is stored until it is proved.
function TwoStep() {
  const { t } = useTranslation();
  const errors = useErrors();
  const session = useSession();
  const [dialog, setDialog] = useState<"recovery" | null>(null);
  const begin = useBeginTotp();
  const setup = begin.data;
  return (
    <Panel
      title={t("security.twoStep.title")}
      action={
        <Badge tone={session.mfa_enabled ? "ok" : "warn"} icon={session.mfa_enabled ? "check" : "alert"}>
          {session.mfa_enabled ? t("security.twoStep.on") : t("security.twoStep.off")}
        </Badge>
      }
    >
      <div className="space-y-4 p-4">
        <p className="max-w-prose text-t2">{t("security.twoStep.body")}</p>
        {begin.isError ? <FormError>{errors.message(begin.error)}</FormError> : null}
        <div className="flex flex-wrap gap-2">
          <Button
            variant={session.mfa_enabled ? "secondary" : "primary"}
            size="md"
            onClick={() => begin.mutate()}
            disabled={begin.isPending}
          >
            <Icon name={session.mfa_enabled ? "refresh" : "shield"} />
            {session.mfa_enabled ? t("security.twoStep.replace") : t("security.twoStep.enable")}
          </Button>
          {session.mfa_enabled ? (
            <Button size="md" onClick={() => setDialog("recovery")}>
              {t("security.twoStep.regenerate")}
            </Button>
          ) : null}
        </div>
      </div>
      {setup ? <TotpSetupDialog setup={setup} onClose={() => begin.reset()} /> : null}
      {dialog === "recovery" ? <RecoveryDialog onClose={() => setDialog(null)} /> : null}
    </Panel>
  );
}

// Passkeys are listed by name and removed with the password; adding one runs the browser's creation ceremony. The
// server decides whether the session is strong enough and returns a replacement session, which the page adopts so
// its CSRF token is the new session's.
function Passkeys() {
  const { t } = useTranslation();
  const session = useSession();
  const list = useQuery(passkeysQuery);
  const actions = usePasskeyActions();
  return (
    <Panel
      title={t("security.passkeys.title")}
      action={
        <Badge tone={list.data?.passkeys.length ? "ok" : "neutral"} icon="key">
          {t("security.passkeys.count", { count: list.data?.passkeys.length ?? 0 })}
        </Badge>
      }
    >
      <div className="space-y-4 p-4">
        <p className="max-w-prose text-t2">{t("security.passkeys.body")}</p>
        {actions.failure ? <FormError>{actions.errors.message(actions.failure)}</FormError> : null}
        {!actions.supported ? <p className="text-t3">{t("security.passkeys.unsupported")}</p> : null}
        <DataState query={list}>
          {(data) => (
            <PasskeyList
              passkeys={data.passkeys}
              busy={actions.busy}
              canRemove={actions.password !== ""}
              onRemove={actions.remove}
            />
          )}
        </DataState>
        {actions.supported ? <AddPasskey actions={actions} /> : null}
        {session.level !== "mfa" && session.mfa_enabled ? (
          <p className="text-label text-t3">{t("security.passkeys.mfaRequired")}</p>
        ) : null}
      </div>
    </Panel>
  );
}

function PasskeyList({
  passkeys,
  busy,
  canRemove,
  onRemove,
}: {
  passkeys: Schemas["Passkey"][];
  busy: boolean;
  canRemove: boolean;
  onRemove: (id: string) => Promise<void>;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  if (passkeys.length === 0) {
    return <p className="text-t3">{t("security.passkeys.empty")}</p>;
  }
  return (
    <ul className="divide-y divide-b1">
      {passkeys.map((passkey) => (
        <li key={passkey.id} className="flex flex-wrap items-center justify-between gap-3 py-3">
          <div className="min-w-0">
            <p className="text-t1 [overflow-wrap:anywhere]">{passkey.name}</p>
            <p className="text-label text-t3">{format.dateTime(new Date(passkey.created_at))}</p>
          </div>
          <Button size="sm" variant="secondary" disabled={busy || !canRemove} onClick={() => void onRemove(passkey.id)}>
            {t("security.passkeys.remove")}
          </Button>
        </li>
      ))}
    </ul>
  );
}

function AddPasskey({ actions }: { actions: PasskeyActions }) {
  const { t } = useTranslation();
  return (
    <div className="grid gap-3 sm:grid-cols-2">
      <TextField label={t("security.passkeys.name")} value={actions.name} onChange={actions.setName} maxLength={100} />
      <TextField
        label={t("security.passkeys.password")}
        type="password"
        autoComplete="current-password"
        value={actions.password}
        onChange={actions.setPassword}
      />
      <div className="sm:col-span-2">
        <Button size="md" disabled={actions.busy || actions.password === ""} onClick={() => void actions.add()}>
          <Icon name="plus" />
          {t("security.passkeys.add")}
        </Button>
      </div>
    </div>
  );
}

type PasskeyActions = ReturnType<typeof usePasskeyActions>;

// usePasskeyChange runs one passkey change: shared busy and failure state, the session the change issued, the recovery
// codes it made (when TOTP is on) and the reloaded list. The change is bound to the session that asked for it: if another
// tab signs someone else in while it is out, the answer is dropped, so the codes are never labelled with the new person.
function usePasskeyChange() {
  const start = useStartSession();
  const show = useShowRecoveryCodes();
  const bound = useSessionBound();
  const client = useQueryClient();
  const [failure, setFailure] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  async function run(change: (signal: AbortSignal) => Promise<{ token: string; codes?: string[] }>) {
    setBusy(true);
    setFailure(null);
    try {
      const answer = await bound(change);
      if (!answer) {
        return;
      }
      const { token, codes } = answer.value;
      // The codes exist only in the answer that made them: show them before refreshing the session, so a failed refresh
      // keeps them on screen and the dialog's Done retries the refresh instead of losing the codes. The host is told the
      // person the change was asked for as; changing a factor rotates this session but not that person.
      if (codes?.length) {
        show({ csrf_token: token, recovery_codes: codes }, answer.claim.identity);
      }
      try {
        await start(token);
      } catch {
        // The dialog holds the codes and retries the session when it closes; the list still reads the new cookie.
      }
      await client.invalidateQueries({ queryKey: passkeysQuery.queryKey });
    } catch (error) {
      setFailure(error);
    } finally {
      setBusy(false);
    }
  }
  return { failure, busy, run };
}

// usePasskeyActions holds the password and name the ceremonies need, and replaces the page's session with the one the
// server issues after a change so the next request carries its CSRF token.
function usePasskeyActions() {
  const errors = useErrors();
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const { failure, busy, run } = usePasskeyChange();
  const supported = typeof window !== "undefined" && "PublicKeyCredential" in window;
  async function add() {
    await run(async (signal) => {
      const begin = await unwrap(
        consoleApi.POST("/api/console/v1/auth/passkeys/register/begin", { body: { name, password }, signal }),
      );
      const credential = await registerPasskey(begin.public_key);
      const added = await unwrap(
        consoleApi.POST("/api/console/v1/auth/passkeys/register/finish", {
          params: { query: { flow_id: begin.flow_id } },
          body: credential,
          signal,
        }),
      );
      setName("");
      setPassword("");
      return { token: "", codes: added.recovery_codes };
    });
  }
  async function remove(id: string) {
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
    failure,
    busy,
    supported,
    add,
    remove,
    setName: (event: ChangeEvent<HTMLInputElement>) => setName(event.target.value),
    setPassword: (event: ChangeEvent<HTMLInputElement>) => setPassword(event.target.value),
  };
}

// A linked provider is another way in, never a way to merge two accounts: linking happens only from here.
function LinkedAccounts() {
  const { t } = useTranslation();
  const site = useQuery(siteQuery);
  const list = useQuery(identitiesQuery);
  const session = useSession();
  const actions = useIdentityActions();
  const providers = site.data?.oauth_providers ?? [];
  const linked = new Map((list.data?.identities ?? []).map((identity) => [identity.provider, identity]));
  // An identity stays listed after its provider is turned off, so it can still be unlinked.
  const names = [...new Set([...providers, ...linked.keys()])];
  if (names.length === 0) {
    return null;
  }
  return (
    <Panel title={t("security.identities.title")}>
      <div className="space-y-4 p-4">
        <p className="max-w-prose text-t2">{t("security.identities.body")}</p>
        {actions.failure ? <FormError>{actions.errors.message(actions.failure)}</FormError> : null}
        <ul className="divide-y divide-b1">
          {names.map((provider) => (
            <IdentityRow
              key={provider}
              provider={provider}
              identity={linked.get(provider)}
              busy={actions.busy}
              needsMfa={session.mfa_enabled && session.level !== "mfa"}
              onLink={actions.link}
              onUnlink={actions.unlink}
            />
          ))}
        </ul>
      </div>
    </Panel>
  );
}

function IdentityRow({
  provider,
  identity,
  busy,
  needsMfa,
  onLink,
  onUnlink,
}: {
  provider: Schemas["OAuthProvider"];
  identity: Schemas["Identity"] | undefined;
  busy: boolean;
  needsMfa: boolean;
  onLink: (provider: Schemas["OAuthProvider"]) => Promise<void>;
  onUnlink: (provider: Schemas["OAuthProvider"]) => Promise<void>;
}) {
  const { t } = useTranslation();
  return (
    <li className="flex flex-wrap items-center justify-between gap-3 py-3">
      <div>
        <p className="text-t1">{t(`security.identities.providers.${provider}`)}</p>
        <p className="text-label text-t3">
          {identity
            ? t("security.identities.linkedAs", { name: identity.display_name })
            : t("security.identities.notLinked")}
        </p>
      </div>
      {identity ? (
        <Button size="sm" variant="secondary" disabled={busy} onClick={() => void onUnlink(provider)}>
          {t("security.identities.unlink")}
        </Button>
      ) : (
        <Button size="sm" disabled={busy || needsMfa} onClick={() => void onLink(provider)}>
          {t("security.identities.link")}
        </Button>
      )}
    </li>
  );
}

function useIdentityActions() {
  const errors = useErrors();
  const start = useStartSession();
  const show = useShowRecoveryCodes();
  const bound = useSessionBound();
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
      if (!answer) {
        return;
      }
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
      const answer = await bound((signal) =>
        unwrap(
          consoleApi.DELETE("/api/console/v1/auth/identities/{provider}", {
            params: { path: { provider } },
            signal,
          }),
        ),
      );
      if (!answer) {
        return;
      }
      const grant = answer.value;
      if (grant.recovery_codes?.length) {
        show({ csrf_token: grant.csrf_token, recovery_codes: grant.recovery_codes }, answer.claim.identity);
      }
      try {
        await start(grant.csrf_token);
      } catch {
        // The codes are on screen and Done retries the session; the list still reads through the new cookie.
      }
      await queryClient.invalidateQueries({ queryKey: identitiesQuery.queryKey });
    } catch (error) {
      setFailure(error);
    } finally {
      setBusy(false);
    }
  }
  return { errors, failure, busy, link, unlink };
}

function Invite() {
  const { t } = useTranslation();
  const { user } = useSession();
  const link = `${window.location.origin}${import.meta.env.BASE_URL}register?invite=${encodeURIComponent(user.invite_code)}`;
  return (
    <Panel title={t("security.account.title")}>
      <dl className="divide-y divide-b1">
        <div className="grid gap-x-4 gap-y-1 px-4 py-3 sm:grid-cols-[12rem_minmax(0,1fr)]">
          <dt className="text-t3">{t("auth.email", { ns: "common" })}</dt>
          <dd className="text-t1 [overflow-wrap:anywhere]">{user.email}</dd>
        </div>
        <div className="grid gap-x-4 gap-y-1 px-4 py-3 sm:grid-cols-[12rem_minmax(0,1fr)]">
          <dt className="text-t3">{t("security.account.invite")}</dt>
          <dd className="min-w-0 space-y-2">
            <p className="font-mono text-t1 [overflow-wrap:anywhere]">{user.invite_code}</p>
            <p className="text-label text-t3 [overflow-wrap:anywhere]">{link}</p>
            <div className="flex items-center gap-3">
              <CopyButton text={link} />
            </div>
          </dd>
        </div>
      </dl>
    </Panel>
  );
}
