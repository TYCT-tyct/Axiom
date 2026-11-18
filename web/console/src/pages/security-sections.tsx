import type { Schemas } from "@axiom/api";
import { useSession } from "@axiom/app";
import { DataState, FormError, useFormat } from "@axiom/ui";
import {
  type InfiniteData,
  type UseInfiniteQueryResult,
  type UseQueryResult,
  useInfiniteQuery,
  useQuery,
} from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { identitiesQuery, siteQuery } from "../queries";
import { clickedRowIndex, useBoardInputs } from "./account-board";
import { AccountActionCard, AccountInputsCard } from "./account-cards";
import {
  buildIdentityRows,
  buildInvitationRows,
  buildPasskeyRows,
  flattenInvitees,
  type InvitationsPage,
  invitationsQuery,
  passkeyRegistrationAvailability,
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

function PasskeysCard({
  wide,
  english,
  actions,
  canRegister,
  rows,
  onRemove,
  onOpenAdd,
}: {
  wide: boolean;
  english: boolean;
  actions: ReturnType<typeof usePasskeyActions>;
  canRegister: boolean;
  rows: ReturnType<typeof buildPasskeyRows>;
  onRemove: (index: number) => void;
  onOpenAdd: () => void;
}) {
  const { t } = useTranslation();
  return (
    <AccountActionCard
      wide={wide}
      english={english}
      title={t("security.passkeys.title")}
      actionLabel={canRegister ? t("security.passkeys.add") : ""}
      actionDisabled={actions.busy}
      onAction={onOpenAdd}
      rowDisabled={actions.busy || actions.password === ""}
      onRowAction={(event) => onRemove(clickedRowIndex(event.target))}
      rows={rows}
    />
  );
}

function passkeysLoc() {
  return typeof window !== "undefined"
    ? { origin: window.location.origin, hostname: window.location.hostname }
    : undefined;
}

function PasskeysLoadedView({
  wide,
  english,
  passkeys,
  actions,
  availability,
  canRegister,
}: {
  wide: boolean;
  english: boolean;
  passkeys: readonly Schemas["Passkey"][];
  actions: ReturnType<typeof usePasskeyActions>;
  availability: ReturnType<typeof passkeyRegistrationAvailability>;
  canRegister: boolean;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  const [addOpen, setAddOpen] = useState(false);
  const serverFailure = actions.failure ? actions.errors.message(actions.failure) : null;
  const shownError = actions.localError ?? serverFailure;

  return (
    <>
      {shownError ? <FormError>{shownError}</FormError> : null}
      {passkeys.length > 0 ? <PasskeyPasswordCard wide={wide} english={english} actions={actions} /> : null}
      <PasskeysCard
        wide={wide}
        english={english}
        actions={actions}
        canRegister={canRegister}
        onOpenAdd={() => setAddOpen(true)}
        onRemove={(index) => {
          const pk = passkeys[index];
          if (pk) void actions.remove(pk.id);
        }}
        rows={buildPasskeyRows(t, format.dateTime, passkeys, availability)}
      />
      {addOpen && canRegister ? (
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

export function PasskeysSection({
  wide,
  english,
  list,
}: {
  wide: boolean;
  english: boolean;
  list: UseQueryResult<Schemas["Passkeys"]>;
}) {
  const { t } = useTranslation();
  const site = useQuery(siteQuery);
  const actions = usePasskeyActions();
  const availability = passkeyRegistrationAvailability(site.data, passkeysLoc(), actions.supported);
  const canRegister = site.isSuccess && availability === "ready";
  const passkeys = list.data?.passkeys;

  if (list.isError) {
    return (
      <DataState query={list} compact>
        {() => null}
      </DataState>
    );
  }
  if (passkeys?.length === 0 && site.isError) {
    return (
      <DataState query={site} compact>
        {() => null}
      </DataState>
    );
  }
  if (!passkeys || (passkeys.length === 0 && site.isPending)) {
    return (
      <PasskeysCard
        wide={wide}
        english={english}
        actions={actions}
        canRegister={false}
        onOpenAdd={() => {}}
        onRemove={() => {}}
        rows={[{ key: "loading", kind: "0", f0: "qses", f1: t("security.loading"), f2: "", f3: "", f4: "" }]}
      />
    );
  }
  return (
    <PasskeysLoadedView
      wide={wide}
      english={english}
      passkeys={passkeys}
      actions={actions}
      availability={site.isSuccess ? availability : "ready"}
      canRegister={canRegister}
    />
  );
}

function IdentitiesLoadedView({
  wide,
  english,
  names,
  linked,
}: {
  wide: boolean;
  english: boolean;
  names: Schemas["OAuthProvider"][];
  linked: Map<Schemas["OAuthProvider"], Schemas["Identity"]>;
}) {
  const { t } = useTranslation();
  const session = useSession();
  const actions = useIdentityActions();
  const [mfaNotice, setMfaNotice] = useState(false);
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

export function IdentitiesSection({ wide, english }: { wide: boolean; english: boolean }) {
  const { t } = useTranslation();
  const site = useQuery(siteQuery);
  const list = useQuery(identitiesQuery);

  if (list.isError) {
    return (
      <DataState query={list} compact>
        {() => null}
      </DataState>
    );
  }
  if (site.isError) {
    return (
      <DataState query={site} compact>
        {() => null}
      </DataState>
    );
  }
  if (!list.isSuccess || !site.isSuccess) {
    return (
      <AccountActionCard
        wide={wide}
        english={english}
        title={t("security.identities.title")}
        rows={[{ key: "loading", kind: "0", f0: "qses", f1: t("security.loading"), f2: "", f3: "", f4: "" }]}
      />
    );
  }

  const providers = site.data.oauth_providers ?? [];
  const linked = new Map(list.data.identities.map((identity) => [identity.provider, identity]));
  const names = [...new Set([...providers, ...linked.keys()])];
  if (names.length === 0) return null;
  return <IdentitiesLoadedView wide={wide} english={english} names={names} linked={linked} />;
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
  const invitations = useInfiniteQuery(invitationsQuery);
  const flat = invitations.isSuccess ? flattenInvitees(invitations.data) : null;
  const fallbackLink = `${window.location.origin}${import.meta.env.BASE_URL}register?invite=${encodeURIComponent(user.invite_code)}`;
  const link = flat?.link || fallbackLink;
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
      <InvitesRecordsCard wide={wide} english={english} invitations={invitations} />
    </>
  );
}

function InvitesRecordsCard({
  wide,
  english,
  invitations,
}: {
  wide: boolean;
  english: boolean;
  invitations: UseInfiniteQueryResult<InfiniteData<InvitationsPage>>;
}) {
  const { t } = useTranslation();
  const format = useFormat();

  if (invitations.isError) {
    return (
      <DataState query={invitations} compact>
        {() => null}
      </DataState>
    );
  }
  if (!invitations.isSuccess) {
    return (
      <AccountActionCard
        wide={wide}
        english={english}
        title={t("security.invitations.title")}
        last
        rows={[{ key: "loading", kind: "0", f0: "qses", f1: t("security.loading"), f2: "", f3: "", f4: "" }]}
      />
    );
  }

  return (
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
      rows={buildInvitationRows(t, format.dateTime, flattenInvitees(invitations.data))}
    />
  );
}
