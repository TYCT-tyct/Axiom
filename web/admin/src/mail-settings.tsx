import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { CheckField, Field } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "./change";
import { Facts, State } from "./parts";
import { mailSettingsQuery } from "./queries";
import { SettingsPage } from "./settings-page";

type Settings = AdminSchemas["MailSettingsUpdate"]["settings"];

// readMail turns the dialog's fields into the request. The port is a whole number; enabled mail needs a server and a
// sender, while disabled mail may keep blanks so the card can still turn it off after an environment change.
function readPort(text: string): number | null {
  const port = Number(text.trim() || "587");
  return Number.isInteger(port) && port >= 1 && port <= 65_535 ? port : null;
}

const optional = (text: string) => (text === "" ? undefined : text);

function readMail(values: Record<string, string>, invalidPort: string, required: string): Settings | Problem {
  const port = readPort(values.port ?? "");
  if (port === null) {
    return { problem: invalidPort };
  }
  const enabled = values.enabled === "true";
  const host = (values.host ?? "").trim();
  const from = (values.from ?? "").trim();
  if (enabled && (!host || !from)) {
    return { problem: required };
  }
  return {
    enabled,
    host: optional(host),
    port,
    username: optional((values.username ?? "").trim()),
    from: optional(from),
    password: optional(values.password ?? ""),
  };
}

export function MailSettings() {
  const query = useQuery(mailSettingsQuery);
  const { t } = useTranslation();
  return (
    <SettingsPage
      query={query}
      labels={{
        title: t("settings.mail.title"),
        unavailableLabel: t("settings.mail.unavailableLabel"),
        unavailable: t("settings.mail.unavailable"),
        edit: t("settings.mail.edit"),
        test: t("settings.mail.test"),
      }}
      facts={({ view, action }) => <MailFacts view={view} action={action} />}
      dialogs={{
        edit: ({ view, onClose }) => <MailEditDialog view={view} onClose={onClose} />,
        test: ({ onClose }) => <MailTestDialog onClose={onClose} />,
      }}
    />
  );
}

function MailFacts({ view, action }: { view: AdminSchemas["MailSettings"]; action: ReactNode }) {
  const { t } = useTranslation();
  const configured = view.enabled === true;
  const rows: [string, ReactNode][] = [
    [
      t("settings.mail.state"),
      <State key="state" tone={configured ? "good" : "neutral"}>
        {t(configured ? "settings.mail.on" : "settings.mail.off")}
      </State>,
    ],
    [t("settings.mail.host"), configured ? `${view.host ?? ""}:${view.port ?? 587}` : "—"],
    [t("settings.mail.username"), view.username || "—"],
    [t("settings.mail.from"), view.from || "—"],
    [t("settings.mail.password"), t(view.has_password ? "settings.mail.passwordSet" : "settings.mail.passwordMissing")],
    [t("settings.mail.source"), t(`settings.mail.sources.${view.source ?? "environment"}`)],
  ];
  if (view.restart_required) {
    rows.push([t("settings.mail.restart"), t("settings.mail.restartBody")]);
  }
  return <Facts title={t("settings.mail.title")} rows={rows} actions={action} />;
}

function MailEditDialog({ view, onClose }: { view: AdminSchemas["MailSettings"]; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation="mail-settings"
      title={t("settings.mail.edit")}
      intro={<p>{t("settings.mail.intro")}</p>}
      action={t("settings.mail.save")}
      fields={(error) => <MailFields view={view} error={error} />}
      read={(values) => readMail(values, t("settings.mail.invalidPort"), t("settings.mail.required"))}
      send={(settings, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/settings/mail", {
            params: { header: { "Idempotency-Key": key } },
            body: { settings, reason },
          }),
        )
      }
      done={(saved) => <MailSaved saved={saved} />}
      onClose={onClose}
    />
  );
}

function MailFields({ view, error }: { view: AdminSchemas["MailSettings"]; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <CheckField
        label={t("settings.mail.enabled")}
        hint={t("settings.mail.enabledHint")}
        name="enabled"
        value="true"
        defaultChecked={view.enabled === true}
      />
      <Field
        label={t("settings.mail.host")}
        name="host"
        autoComplete="off"
        defaultValue={view.host ?? ""}
        error={errors.field(error, "settings/host")}
      />
      <Field
        label={t("settings.mail.port")}
        hint={t("settings.mail.portHint")}
        name="port"
        inputMode="numeric"
        autoComplete="off"
        defaultValue={view.port ?? 587}
        error={errors.field(error, "settings")}
      />
      <Field
        label={t("settings.mail.username")}
        name="username"
        autoComplete="off"
        defaultValue={view.username ?? ""}
        error={errors.field(error, "settings")}
      />
      <Field
        label={t("settings.mail.from")}
        name="from"
        inputMode="email"
        autoComplete="off"
        defaultValue={view.from ?? ""}
        error={errors.field(error, "settings")}
      />
      <Field
        label={t("settings.mail.password")}
        hint={t("settings.mail.passwordHint")}
        name="password"
        type="password"
        autoComplete="new-password"
        error={errors.field(error, "settings")}
      />
    </>
  );
}

// mailSavedRestart is the save's own result: a database change needs a restart before the running node uses it.
export function mailSavedRestart(saved: AdminSchemas["MailSettings"]): boolean {
  return saved.restart_required === true;
}

function MailSaved({ saved }: { saved: AdminSchemas["MailSettings"] }) {
  const { t } = useTranslation();
  return (
    <>
      <p>{t("settings.mail.saved")}</p>
      {mailSavedRestart(saved) ? <p>{t("settings.mail.restartSaved")}</p> : null}
    </>
  );
}

function MailTestDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <ChangeDialog
      operation="mail-settings-test"
      title={t("settings.mail.testTitle")}
      intro={<p>{t("settings.mail.testIntro")}</p>}
      action={t("settings.mail.send")}
      verdict
      fields={(error) => (
        <Field
          label={t("settings.mail.to")}
          name="to"
          inputMode="email"
          autoComplete="off"
          error={errors.field(error, "to")}
        />
      )}
      read={(values): { to: string } | Problem => {
        const to = (values.to ?? "").trim();
        return to ? { to } : { problem: t("settings.mail.invalidTo") };
      }}
      send={(input, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/settings/mail/tests", {
            params: { header: { "Idempotency-Key": key } },
            body: { to: input.to, reason },
          }),
        )
      }
      done={(result) => <p>{t(`settings.mail.status.${result.status ?? "failed"}`)}</p>}
      onClose={onClose}
    />
  );
}
