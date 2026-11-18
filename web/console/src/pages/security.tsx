import { consoleApi, unwrap, unwrapEmpty } from "@axiom/api";
import {
  claimSession,
  holdsIdentity,
  holdsSession,
  RecoveryDialog,
  TotpSetupDialog,
  useBeginTotp,
  useErrors,
  useSession,
} from "@axiom/app";
import DesktopSecurity from "@axiom/boards/AxApp/regions/Security";
import DesktopSecurityEn from "@axiom/boards/AxApp/regions/Security.en";
import DesktopSecurityZh from "@axiom/boards/AxApp/regions/Security.zh";
import PhoneSecurity from "@axiom/boards/AxAppPhone/regions/Security";
import PhoneSecurityEn from "@axiom/boards/AxAppPhone/regions/Security.en";
import PhoneSecurityZh from "@axiom/boards/AxAppPhone/regions/Security.zh";
import { Button, Cluster, DataState, FormError, Notice, useFormat, usePageTitle } from "@axiom/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type SyntheticEvent, useState } from "react";
import { useTranslation } from "react-i18next";
import { ConfirmDialog } from "../confirm-dialog";
import { usePendingCodes } from "../pending-codes";
import { passkeysQuery } from "../queries";
import { boardPageStyle, clickedRowIndex, makeBoardT, useEnglish, useWide } from "./account-board";
import {
  buildAuthRows,
  buildSessionRows,
  type SessionsState,
  sessionsQuery,
  type TotpStatus,
  totpStatus,
} from "./security-data";
import { ChangePasswordDialog, ReplaceTotpDialog } from "./security-dialogs";
import { IdentitiesSection, InvitesSection, PasskeysSection } from "./security-sections";

export default function Security() {
  const { t } = useTranslation();
  const wide = useWide();
  const english = useEnglish();
  const session = useSession();
  const passkeys = useQuery(passkeysQuery);
  const status = totpStatus(session.mfa_enabled, passkeys.isSuccess ? passkeys.data.passkeys.length : undefined);
  const beginEnable = useBeginTotp();
  const beginReplace = useBeginTotp();
  const [passwordOpen, setPasswordOpen] = useState(false);
  const [recoveryOpen, setRecoveryOpen] = useState(false);
  const [banner, setBanner] = useState<string | null>(null);
  const [confirmRevokeAll, setConfirmRevokeAll] = useState(false);

  usePageTitle(t("nav.security"));

  const sessions = useSessionsControls(setBanner, () => setConfirmRevokeAll(false));

  return (
    <>
      <SecurityBanners
        banner={banner}
        canRegenerate={status !== "off"}
        onDismissBanner={() => setBanner(null)}
        onRegenerate={() => setRecoveryOpen(true)}
        beginError={beginEnable.error ?? beginReplace.error}
        sessionError={sessions.actionError}
      />
      <SecurityMainCard
        wide={wide}
        english={english}
        status={status}
        sessions={sessions}
        authBusy={beginEnable.isPending || beginReplace.isPending}
        onOpenPassword={() => setPasswordOpen(true)}
        onOpenTotp={(action) => (action === "replace" ? beginReplace : beginEnable).mutate()}
        onOpenRecovery={() => setRecoveryOpen(true)}
        onOpenRevokeAll={() => setConfirmRevokeAll(true)}
      />
      <PasskeysSection wide={wide} english={english} list={passkeys} />
      <IdentitiesSection wide={wide} english={english} />
      <InvitesSection wide={wide} english={english} onNotice={setBanner} />
      <SecurityModals
        english={english}
        passwordOpen={passwordOpen}
        setPasswordOpen={setPasswordOpen}
        recoveryOpen={recoveryOpen}
        setRecoveryOpen={setRecoveryOpen}
        confirmRevokeAll={confirmRevokeAll}
        setConfirmRevokeAll={setConfirmRevokeAll}
        beginEnable={beginEnable}
        beginReplace={beginReplace}
        sessions={sessions}
        setBanner={setBanner}
      />
    </>
  );
}

function SecurityMainCard({
  wide,
  english,
  status,
  sessions,
  authBusy,
  onOpenPassword,
  onOpenTotp,
  onOpenRecovery,
  onOpenRevokeAll,
}: {
  wide: boolean;
  english: boolean;
  status: TotpStatus;
  sessions: ReturnType<typeof useSessionsControls>;
  authBusy: boolean;
  onOpenPassword: () => void;
  onOpenTotp: (action: "enable" | "replace") => void;
  onOpenRecovery: () => void;
  onOpenRevokeAll: () => void;
}) {
  const { t } = useTranslation();
  const SecurityRegion = wide ? DesktopSecurity : PhoneSecurity;
  const authRows = buildAuthRows(t, status);
  const sessionRows = buildSessionRows(
    t,
    sessions.formatDateTime,
    sessions.query.isSuccess ? { status: "ready", data: sessions.query.data } : { status: "pending" },
  );

  return (
    <>
      <SecurityRegion
        t={makeBoardT(english, wide ? DesktopSecurityEn : PhoneSecurityEn, wide ? DesktopSecurityZh : PhoneSecurityZh, {
          mc70239e4f3: t("security.description"),
        })}
        values={{
          pageStyle: boardPageStyle(wide, { first: true }),
          onAuthAction: (event) => {
            const key = authRows[clickedRowIndex(event.target)]?.key;
            if (key === "password") onOpenPassword();
            if (key === "enable" || key === "replace") onOpenTotp(key);
            if (key === "recovery") onOpenRecovery();
          },
          authActionDisabled: authBusy,
          hideCard2: sessions.query.isError,
          hideRevokeOthers: !sessions.query.isSuccess || !sessions.query.data.available || sessions.otherCount === 0,
          revokeOthersDisabled: sessions.busy,
          onRevokeOthers: onOpenRevokeAll,
          sessionRevokeDisabled: sessions.busy,
          onSessionRevoke: sessions.onSessionRevoke,
        }}
        rows={{ authRows0: authRows, sessions0: sessionRows }}
        text={{ sessionsTitle: t("security.sessions.title"), sessionsClearLabel: t("security.sessions.revokeAll") }}
      />
      {sessions.query.isError ? (
        <DataState query={sessions.query} compact>
          {() => null}
        </DataState>
      ) : null}
    </>
  );
}

function SecurityModals({
  english,
  passwordOpen,
  setPasswordOpen,
  recoveryOpen,
  setRecoveryOpen,
  confirmRevokeAll,
  setConfirmRevokeAll,
  beginEnable,
  beginReplace,
  sessions,
  setBanner,
}: {
  english: boolean;
  passwordOpen: boolean;
  setPasswordOpen: (open: boolean) => void;
  recoveryOpen: boolean;
  setRecoveryOpen: (open: boolean) => void;
  confirmRevokeAll: boolean;
  setConfirmRevokeAll: (open: boolean) => void;
  beginEnable: ReturnType<typeof useBeginTotp>;
  beginReplace: ReturnType<typeof useBeginTotp>;
  sessions: ReturnType<typeof useSessionsControls>;
  setBanner: (message: string) => void;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const refreshSessions = () => void client.invalidateQueries({ queryKey: sessionsQuery.queryKey });
  return (
    <>
      {passwordOpen ? (
        <ChangePasswordDialog
          english={english}
          onClose={() => setPasswordOpen(false)}
          onSaved={() => setBanner(t("security.password.saved"))}
        />
      ) : null}
      {beginEnable.data ? (
        <TotpSetupDialog
          setup={beginEnable.data}
          onClose={() => {
            beginEnable.reset();
            refreshSessions();
          }}
        />
      ) : null}
      {beginReplace.data ? (
        <ReplaceTotpDialog
          setup={beginReplace.data}
          onClose={() => beginReplace.reset()}
          onReplaced={() => setBanner(t("security.twoStep.replaced"))}
        />
      ) : null}
      {recoveryOpen ? (
        <RecoveryDialog
          onClose={() => {
            setRecoveryOpen(false);
            refreshSessions();
          }}
        />
      ) : null}
      {confirmRevokeAll ? (
        <ConfirmDialog
          title={t("security.sessions.revokeAllTitle")}
          body={t("security.sessions.revokeAllBody")}
          confirm={t("security.sessions.revokeAllConfirm")}
          action={sessions.revokeAll}
          onClose={() => setConfirmRevokeAll(false)}
        />
      ) : null}
    </>
  );
}

function SecurityBanners({
  banner,
  canRegenerate,
  onDismissBanner,
  onRegenerate,
  beginError,
  sessionError,
}: {
  banner: string | null;
  canRegenerate: boolean;
  onDismissBanner: () => void;
  onRegenerate: () => void;
  beginError: unknown;
  sessionError: unknown;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const pending = usePendingCodes();
  const oauth = new URLSearchParams(window.location.search).get("oauth");

  return (
    <>
      {oauth === "identity_linked" || oauth === "identity_already_linked" ? (
        <FormError>{t(`security.oauth.${oauth}`)}</FormError>
      ) : null}
      {pending?.failure ? (
        <FormError>
          <p>{t("security.pendingCodes.failed")}</p>
          <p>{errors.message(pending.failure)}</p>
          <Cluster gap={8}>
            <Button size="sm" onClick={pending.retry} disabled={pending.pending}>
              {t("security.pendingCodes.retry")}
            </Button>
            {canRegenerate ? (
              <Button size="sm" onClick={onRegenerate}>
                {t("security.pendingCodes.regenerate")}
              </Button>
            ) : null}
          </Cluster>
        </FormError>
      ) : null}
      {banner ? (
        <Notice action={<Button onClick={onDismissBanner}>{t("security.close")}</Button>}>{banner}</Notice>
      ) : null}
      {beginError ? <FormError>{errors.message(beginError)}</FormError> : null}
      {sessionError ? <FormError>{errors.message(sessionError)}</FormError> : null}
    </>
  );
}

function useSessionsControls(onBanner: (message: string) => void, closeConfirm: () => void) {
  const { t } = useTranslation();
  const format = useFormat();
  const client = useQueryClient();
  const query = useQuery(sessionsQuery);
  const sessions = query.data?.sessions ?? [];
  const otherCount = sessions.filter((item) => !item.current).length;

  const revokeOne = useMutation({
    onMutate: () => claimSession(client),
    mutationFn: (sessionId: string) =>
      unwrapEmpty(
        consoleApi.DELETE("/api/console/v1/sessions/{session_id}", { params: { path: { session_id: sessionId } } }),
      ),
    onSuccess: (_res, sessionId, claim) => {
      if (!claim || !holdsIdentity(client, claim.identity)) return;
      if (holdsSession(client, claim.binding)) {
        client.setQueryData<SessionsState>(sessionsQuery.queryKey, (prev) =>
          prev ? { ...prev, sessions: prev.sessions.filter((item) => item.id !== sessionId) } : prev,
        );
      }
      void client.invalidateQueries({ queryKey: sessionsQuery.queryKey });
      onBanner(t("security.sessions.revokedOne"));
    },
  });

  const revokeAll = useMutation({
    onMutate: () => claimSession(client),
    mutationFn: () => unwrap(consoleApi.DELETE("/api/console/v1/sessions")),
    onSuccess: (res, _vars, claim) => {
      if (!claim || !holdsIdentity(client, claim.identity)) return;
      const ended = res.revoked ?? otherCount;
      if (holdsSession(client, claim.binding)) {
        client.setQueryData<SessionsState>(sessionsQuery.queryKey, (prev) =>
          prev ? { ...prev, sessions: prev.sessions.filter((item) => item.current) } : prev,
        );
      }
      void client.invalidateQueries({ queryKey: sessionsQuery.queryKey });
      closeConfirm();
      onBanner(t("security.sessions.revokedAll", { count: ended }));
    },
  });

  return {
    query,
    otherCount,
    formatDateTime: format.dateTime,
    busy: revokeOne.isPending || revokeAll.isPending,
    actionError: revokeOne.error ?? revokeAll.error,
    revokeAll,
    onSessionRevoke: (event: SyntheticEvent) => {
      const item = sessions[clickedRowIndex(event.target)];
      if (item && !item.current && item.id && !revokeOne.isPending) revokeOne.mutate(item.id);
    },
  };
}
