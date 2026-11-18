import { useSession } from "@axiom/app";
import { DataState, FormError, useFormat } from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { identitiesQuery, passkeysQuery, siteQuery } from "../queries";
import { clickedRowIndex, useBoardInputs } from "./account-board";
import { AccountActionCard, AccountInputsCard } from "./account-cards";
import {
  buildIdentityRows,
  buildInvitationRows,
  buildPasskeyRows,
  flattenInvitees,
  invitationsQuery,
  useIdentityActions,
  usePasskeyActions,
} from "./security-data";
import { AddPasskeyDialog } from "./security-dialogs";

function PasskeyPasswordCard({
  wide,
  english,
  actions,
}: {
  wide: boolean;
  english: boolean;
  actions: ReturnType<typeof usePasskeyActions>;
}) {
  const { t } = useTranslation();
  const session = useSession();
  const inputs = useBoardInputs({ "current-password": { type: "password", autoComplete: "current-password" } });

  useEffect(() => {
    inputs.setValue("current-password", actions.password);
  }, [actions.password, inputs]);

  return (
    <AccountInputsCard
      wide={wide}
      english={english}
      inputRef={inputs.inputRef}
      disabled={actions.busy}
      onInput={(event) => {
        if (event.target instanceof HTMLInputElement && event.target.dataset.k === "current-password") {
          actions.setPasswordValue(event.target.value);
        }
      }}
      rows={[
        {
          key: "current-password",
          kind: "2",
          f0: "qrow",
          f1: "qrl",
          f2: t("security.passkeys.password"),
          f3:
            session.level !== "mfa" && session.mfa_enabled
              ? t("security.passkeys.mfaRequired")
              : t("security.passkeys.passwordHint"),
          f4: "qrc",
          f5: "qin",
          f6: "current-password",
          f7: actions.password,
          f8: t("security.passkeys.password"),
        },
      ]}
    />
  );
}

export function PasskeysSection({ wide, english }: { wide: boolean; english: boolean }) {
  const { t } = useTranslation();
  const format = useFormat();
  const list = useQuery(passkeysQuery);
  const actions = usePasskeyActions();
  const [addOpen, setAddOpen] = useState(false);
  const passkeys = list.data?.passkeys ?? [];
  const serverFailure = actions.failure ? actions.errors.message(actions.failure) : null;

  return (
    <>
      {(actions.localError ?? serverFailure) ? <FormError>{actions.localError ?? serverFailure}</FormError> : null}
      {list.isError ? (
        <DataState query={list} compact>
          {() => null}
        </DataState>
      ) : null}
      {passkeys.length > 0 ? <PasskeyPasswordCard wide={wide} english={english} actions={actions} /> : null}
      <AccountActionCard
        wide={wide}
        english={english}
        title={t("security.passkeys.title")}
        actionLabel={actions.supported ? t("security.passkeys.add") : ""}
        actionDisabled={actions.busy}
        onAction={() => setAddOpen(true)}
        rowDisabled={actions.busy || actions.password === ""}
        onRowAction={(event) => {
          const pk = passkeys[clickedRowIndex(event.target)];
          if (pk) void actions.remove(pk.id);
        }}
        rows={buildPasskeyRows(t, format.dateTime, passkeys, actions.supported)}
      />
      {addOpen ? (
        <AddPasskeyDialog
          initialPassword={actions.password}
          busy={actions.busy}
          errorText={serverFailure}
          onSubmit={actions.add}
          onClose={() => setAddOpen(false)}
        />
      ) : null}
    </>
  );
}

export function IdentitiesSection({ wide, english }: { wide: boolean; english: boolean }) {
  const { t } = useTranslation();
  const site = useQuery(siteQuery);
  const list = useQuery(identitiesQuery);
  const session = useSession();
  const actions = useIdentityActions();
  const [mfaNotice, setMfaNotice] = useState(false);
  const providers = site.data?.oauth_providers ?? [];
  const linked = new Map((list.data?.identities ?? []).map((identity) => [identity.provider, identity]));
  const names = [...new Set([...providers, ...linked.keys()])];
  if (names.length === 0) return null;

  const needsMfa = session.mfa_enabled && session.level !== "mfa";

  return (
    <>
      {actions.failure || mfaNotice ? (
        <FormError>
          {actions.failure ? actions.errors.message(actions.failure) : t("security.passkeys.mfaRequired")}
        </FormError>
      ) : null}
      <AccountActionCard
        wide={wide}
        english={english}
        title={t("security.identities.title")}
        rowDisabled={actions.busy}
        onRowAction={(event) => {
          const provider = names[clickedRowIndex(event.target)];
          if (!provider || actions.busy) return;
          if (linked.has(provider)) {
            setMfaNotice(false);
            void actions.unlink(provider);
          } else if (needsMfa) setMfaNotice(true);
          else {
            setMfaNotice(false);
            void actions.link(provider);
          }
        }}
        rows={buildIdentityRows(t, names, linked)}
      />
    </>
  );
}

function useCopyInviteLink(link: string, onNotice: (message: string) => void) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const copyLink = () => {
    if (!navigator.clipboard) return onNotice(t("security.invitations.copyFailed"));
    navigator.clipboard.writeText(link).then(
      () => {
        setCopied(true);
        setTimeout(() => setCopied(false), 3000);
      },
      () => onNotice(t("security.invitations.copyFailed")),
    );
  };
  return { copied, copyLink };
}

export function InvitesSection({
  wide,
  english,
  onNotice,
}: {
  wide: boolean;
  english: boolean;
  onNotice: (message: string) => void;
}) {
  const { t } = useTranslation();
  const { user } = useSession();
  const link = `${window.location.origin}${import.meta.env.BASE_URL}register?invite=${encodeURIComponent(user.invite_code)}`;
  const { copied, copyLink } = useCopyInviteLink(link, onNotice);

  return (
    <>
      <AccountActionCard
        wide={wide}
        english={english}
        title={t("security.account.title")}
        actionLabel={copied ? t("security.invitations.copied") : t("security.invitations.copy")}
        onAction={copyLink}
        rows={[
          {
            key: "email",
            kind: "0",
            f0: "qses",
            f1: t("auth.email", { ns: "common" }),
            f2: user.email,
            f3: "",
            f4: "",
          },
          {
            key: "invite-code",
            kind: "0",
            f0: "qses",
            f1: t("security.account.invite"),
            f2: link,
            f3: "qtag",
            f4: user.invite_code,
          },
        ]}
      />
      <InvitesRecordsCard wide={wide} english={english} />
    </>
  );
}

function InvitesRecordsCard({ wide, english }: { wide: boolean; english: boolean }) {
  const { t } = useTranslation();
  const format = useFormat();
  const invitations = useInfiniteQuery(invitationsQuery);
  const flat = flattenInvitees(invitations.data);
  return (
    <>
      {invitations.isError ? (
        <DataState query={invitations} compact>
          {() => null}
        </DataState>
      ) : null}
      <AccountActionCard
        wide={wide}
        english={english}
        title={t("security.invitations.title")}
        last
        actionLabel={
          invitations.hasNextPage
            ? invitations.isFetchingNextPage
              ? t("security.loading")
              : t("security.invitations.loadMore")
            : ""
        }
        actionDisabled={invitations.isFetchingNextPage}
        onAction={() => void invitations.fetchNextPage()}
        rows={buildInvitationRows(t, format.dateTime, flat)}
      />
    </>
  );
}
