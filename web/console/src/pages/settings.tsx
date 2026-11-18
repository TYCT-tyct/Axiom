import { ApiError, consoleApi, type Schemas, unwrapEmpty } from "@axiom/api";
import { can, claimSession, holdsIdentity, useErrors, useSession } from "@axiom/app";
import DesktopProfile from "@axiom/boards/AxApp/regions/Profile";
import DesktopProfileEn from "@axiom/boards/AxApp/regions/Profile.en";
import DesktopProfileZh from "@axiom/boards/AxApp/regions/Profile.zh";
import PhoneProfile from "@axiom/boards/AxAppPhone/regions/Profile";
import PhoneProfileEn from "@axiom/boards/AxAppPhone/regions/Profile.en";
import PhoneProfileZh from "@axiom/boards/AxAppPhone/regions/Profile.zh";
import {
  Button,
  Cluster,
  chooseLanguage,
  DataState,
  Dialog,
  Field,
  FormError,
  Notice,
  Stack,
  SubmitButton,
  usePageTitle,
  usePreferences,
} from "@axiom/ui";
import { type UseQueryResult, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { type FormEvent, type SyntheticEvent, useState } from "react";
import { useTranslation } from "react-i18next";
import { billingPreferenceChoices, useSaveBillingPreference } from "../billing-preference";
import { billingPreferenceQuery } from "../queries";
import { boardPageStyle, clickedRowIndex, makeBoardT, useEnglish, useWide } from "./account-board";
import { AccountActionCard } from "./account-cards";
import { type EmailConfirmStatus, useEmailConfirm } from "./settings-confirm";
import {
  buildBillingRows,
  buildLanguageRows,
  buildProfileActionRows,
  buildProfileRows,
  buildThemeRows,
  type DisplayNameState,
  displayNameQuery,
  useProfileActions,
  validateEmailChange,
} from "./settings-data";

const themes = ["system", "light", "dark"] as const;
const emailIssueKeys = {
  invalid: "settings.profile.emailInvalid",
  unchanged: "settings.profile.emailUnchanged",
  passwordRequired: "settings.profile.emailPasswordRequired",
} as const;

export default function Settings() {
  const { t } = useTranslation();
  const wide = useWide();
  const english = useEnglish();
  const session = useSession();
  const errors = useErrors();
  const workspacesVisible = can(session, "api_keys.view");
  const displayName = useQuery(displayNameQuery);
  const confirm = useEmailConfirm();
  const profile = useProfileActions(displayName.data, session.user.email);
  const [notice, setNotice] = useState<string | null>(null);
  const [billingError, setBillingError] = useState<unknown>(null);

  usePageTitle(t("nav.settings"));

  return (
    <>
      <SettingsAlerts
        notice={confirm.status.state === "confirmed" ? null : (notice ?? profile.banner)}
        confirmStatus={confirm.status}
        billingError={billingError}
        onDismiss={() => {
          setNotice(null);
          profile.clearBanner();
          confirm.dismiss();
        }}
        errorMessage={errors.message}
      />
      <ProfileSection
        wide={wide}
        english={english}
        displayName={displayName}
        email={session.user.email}
        profile={profile}
      />
      <AppearanceSection wide={wide} english={english} />
      <BillingAndWorkspaces
        wide={wide}
        english={english}
        workspacesVisible={workspacesVisible}
        onNotice={setNotice}
        onBillingError={setBillingError}
      />
      {profile.emailModalOpen ? (
        <EmailChangeDialog
          currentEmail={session.user.email}
          initialEmail={profile.emailDraft}
          english={english}
          onClose={() => profile.setEmailModalOpen(false)}
          onSent={profile.onEmailSent}
        />
      ) : null}
    </>
  );
}

function BillingAndWorkspaces({
  wide,
  english,
  workspacesVisible,
  onNotice,
  onBillingError,
}: {
  wide: boolean;
  english: boolean;
  workspacesVisible: boolean;
  onNotice: (message: string | null) => void;
  onBillingError: (error: unknown) => void;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const preference = useQuery(billingPreferenceQuery);
  const saveBilling = useSaveBillingPreference(() => {
    onBillingError(null);
    onNotice(t("settings.saved"));
  }, onBillingError);
  return (
    <>
      <BillingSection
        wide={wide}
        english={english}
        hasWorkspaces={workspacesVisible}
        preference={preference}
        saving={saveBilling.isPending}
        onBillingClick={(event) => {
          const choice = billingPreferenceChoices[clickedRowIndex(event.target)];
          if (choice && !saveBilling.isPending) saveBilling.mutate(choice);
        }}
      />
      {workspacesVisible ? (
        <AccountActionCard
          wide={wide}
          english={english}
          title={t("workspaces.settings.title")}
          last
          onRowAction={() => void navigate({ to: "/workspaces" })}
          rows={[
            {
              key: "workspaces",
              kind: "1",
              f0: "qses",
              f1: t("workspaces.settings.title"),
              f2: t("workspaces.settings.body"),
              f3: "button",
              f4: "btn sec sm ",
              f5: t("workspaces.settings.link"),
            },
          ]}
        />
      ) : null}
    </>
  );
}

function ProfileSection({
  wide,
  english,
  displayName,
  email,
  profile,
}: {
  wide: boolean;
  english: boolean;
  displayName: UseQueryResult<DisplayNameState>;
  email: string;
  profile: ReturnType<typeof useProfileActions>;
}) {
  const { t } = useTranslation();
  const ProfileRegion = wide ? DesktopProfile : PhoneProfile;
  const profileT = makeBoardT(
    english,
    wide ? DesktopProfileEn : PhoneProfileEn,
    wide ? DesktopProfileZh : PhoneProfileZh,
    {
      m47cc03e27e: t("settings.description"),
    },
  );
  const serverName = displayName.data?.available ? displayName.data.name : "";
  const nameAvailable = displayName.data?.available !== false;
  const actionRows = buildProfileActionRows({
    t,
    nameAvailable,
    nameFeedback: profile.nameMessage,
    emailSentFeedback: profile.emailSentTo ? t("settings.profile.emailSent", { email: profile.emailSentTo }) : null,
  });

  return (
    <>
      {displayName.isError ? (
        <DataState query={displayName} compact>
          {() => null}
        </DataState>
      ) : null}
      <ProfileRegion
        t={profileT}
        values={{
          pageStyle: boardPageStyle(wide, { first: true }),
          hideDeletion: true,
          inputRef: profile.inputs.inputRef,
          onInput: profile.onInput,
          onKeyDown: profile.onKeyDown,
          inputDisabled: profile.nameSaving,
        }}
        rows={{
          rows0: buildProfileRows({
            t,
            name: serverName,
            email,
            nameAvailable,
            nameFeedback: profile.nameMessage,
          }),
        }}
      />
      <AccountActionCard
        wide={wide}
        english={english}
        title={t("settings.profile.actionsTitle")}
        rowDisabled={profile.nameSaving}
        onRowAction={(event) => profile.onActionIndex(actionRows)(clickedRowIndex(event.target))}
        rows={actionRows}
      />
    </>
  );
}

function SettingsAlerts({
  notice,
  confirmStatus,
  billingError,
  onDismiss,
  errorMessage,
}: {
  notice: string | null;
  confirmStatus: EmailConfirmStatus;
  billingError: unknown;
  onDismiss: () => void;
  errorMessage: (error: unknown) => string;
}) {
  const { t } = useTranslation();
  return (
    <>
      {confirmStatus.state === "pending" ? <Notice>{t("settings.profile.emailConfirming")}</Notice> : null}
      {confirmStatus.state === "confirmed" ? (
        <Notice action={<Button onClick={onDismiss}>{t("settings.dismiss")}</Button>}>
          {t("settings.profile.emailConfirmed")}
        </Notice>
      ) : null}
      {confirmStatus.state === "error" ? <FormError>{errorMessage(confirmStatus.error)}</FormError> : null}
      {notice ? <Notice action={<Button onClick={onDismiss}>{t("settings.dismiss")}</Button>}>{notice}</Notice> : null}
      {billingError ? <FormError>{errorMessage(billingError)}</FormError> : null}
    </>
  );
}

function AppearanceSection({ wide, english }: { wide: boolean; english: boolean }) {
  const { t } = useTranslation();
  const { theme, chooseTheme } = usePreferences();
  return (
    <>
      <AccountActionCard
        wide={wide}
        english={english}
        title={t("settings.appearance.themeTitle")}
        onRowAction={(event) => {
          const picked = themes[clickedRowIndex(event.target)];
          if (picked) chooseTheme(picked);
        }}
        rows={buildThemeRows(t, theme)}
      />
      <AccountActionCard
        wide={wide}
        english={english}
        title={t("settings.appearance.languageTitle")}
        onRowAction={(event) => chooseLanguage(clickedRowIndex(event.target) === 0 ? "zh" : "en")}
        rows={buildLanguageRows(t, english)}
      />
    </>
  );
}

function BillingSection({
  wide,
  english,
  hasWorkspaces,
  preference,
  saving,
  onBillingClick,
}: {
  wide: boolean;
  english: boolean;
  hasWorkspaces: boolean;
  preference: UseQueryResult<Schemas["BillingPreference"]>;
  saving: boolean;
  onBillingClick: (event: SyntheticEvent) => void;
}) {
  const { t } = useTranslation();
  if (preference.status !== "success") {
    return (
      <DataState query={preference} compact>
        {() => null}
      </DataState>
    );
  }
  return (
    <AccountActionCard
      wide={wide}
      english={english}
      title={t("settings.billing.title")}
      last={!hasWorkspaces}
      rowDisabled={saving}
      onRowAction={onBillingClick}
      rows={buildBillingRows(t, preference.data)}
    />
  );
}

function useSendEmailChange(onSent: (address: string) => void) {
  const client = useQueryClient();
  return useMutation({
    onMutate: () => claimSession(client),
    mutationFn: (body: { email: string; current_password: string; language: "zh" | "en" }) =>
      unwrapEmpty(consoleApi.POST("/api/console/v1/profile/email", { body })),
    onSuccess: (_res, variables, claim) => {
      if (!claim || !holdsIdentity(client, claim.identity)) return;
      onSent(variables.email);
    },
  });
}

function EmailChangeDialog({
  currentEmail,
  initialEmail,
  english,
  onClose,
  onSent,
}: {
  currentEmail: string;
  initialEmail: string;
  english: boolean;
  onClose: () => void;
  onSent: (address: string) => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [email, setEmail] = useState(initialEmail === currentEmail ? "" : initialEmail);
  const [password, setPassword] = useState("");
  const [validationError, setValidationError] = useState<string | null>(null);
  const send = useSendEmailChange(onSent);

  function submit(event: FormEvent) {
    event.preventDefault();
    if (send.isPending) return;
    const issue = validateEmailChange(email, currentEmail, password);
    if (issue) return setValidationError(t(emailIssueKeys[issue]));
    setValidationError(null);
    send.mutate({ email: email.trim(), current_password: password, language: english ? "en" : "zh" });
  }

  const serverError =
    send.error instanceof ApiError && send.error.code === "invalid_credentials"
      ? t("settings.profile.emailWrongPassword")
      : send.isError
        ? errors.message(send.error)
        : null;

  return (
    <Dialog title={t("settings.profile.emailChangeTitle")} onClose={onClose} dismissible={!send.isPending}>
      <form onSubmit={submit} noValidate>
        <Stack gap={16}>
          <p>{t("settings.profile.emailChangeBody")}</p>
          {(validationError ?? serverError) ? <FormError>{validationError ?? serverError}</FormError> : null}
          <Field
            label={t("settings.profile.emailNew")}
            type="email"
            autoComplete="email"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            error={errors.field(send.error, "email")}
          />
          <Field
            label={t("settings.profile.emailCurrentPassword")}
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            error={errors.field(send.error, "current_password")}
          />
          <Cluster justify="end" gap={8}>
            <Button onClick={onClose} disabled={send.isPending}>
              {t("settings.cancel")}
            </Button>
            <SubmitButton pending={send.isPending}>{t("settings.profile.emailSubmit")}</SubmitButton>
          </Cluster>
        </Stack>
      </form>
    </Dialog>
  );
}
