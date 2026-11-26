import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { ActionButton, DataState, Field, PageHead, SettingRow, Tag, Text } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, isProblem, type Problem } from "../change";
import { mailSavedRestart, readMail } from "../mail-read";
import { PageBody } from "../parts";
import { mailSettingsQuery } from "../queries";
import { NumberSetting, SecretSetting, SettingsCard, SwitchSetting, TextSetting } from "../setting-rows";
import { SettingsSubmit, type SettingValue, useSettingDrafts } from "../settings-drafts";
import { say } from "../toasts";

type Settings = AdminSchemas["MailSettings"];
type Key = "mail.enabled" | "mail.host" | "mail.port" | "mail.username" | "mail.password" | "mail.from";

export default function MailSettings() {
  const { t } = useTranslation();
  const query = useQuery(mailSettingsQuery);
  if (query.data === undefined) {
    return (
      <>
        <PageHead title={t("shell.nav.mail")} lede={t("settings.pages.mail.lede")} />
        <PageBody>
          <DataState query={query}>{() => null}</DataState>
        </PageBody>
      </>
    );
  }
  return <Rows view={query.data} />;
}

const savedOf = (view: Settings): Record<Key, SettingValue> => ({
  "mail.enabled": view.enabled === true,
  "mail.host": view.host ?? "",
  "mail.port": String(view.port ?? 587),
  "mail.username": view.username ?? "",
  // a saved password is never read; a new one is typed
  "mail.password": "",
  "mail.from": view.from ?? "",
});

// The form's text of each row, as readMail reads it.
const textsOf = (values: Record<Key, SettingValue>) => ({
  enabled: String(values["mail.enabled"] === true),
  host: String(values["mail.host"]),
  port: String(values["mail.port"]),
  username: String(values["mail.username"]),
  from: String(values["mail.from"]),
  password: String(values["mail.password"]),
});

function useMailDrafts(view: Settings) {
  const { t } = useTranslation();
  const words = { port: t("settings.mail.invalidPort"), required: t("settings.mail.required") };
  return useSettingDrafts<Key>({
    scope: "settings:mail",
    saved: savedOf(view),
    check: (key, value) => key !== "mail.port" || /^\d+$/.test(String(value).trim()),
    show: (key, value) => {
      if (key !== "mail.password") return undefined;
      if (value !== "") return t("drafts.secretNew");
      return t(view.has_password ? "drafts.secretSet" : "drafts.secretUnset");
    },
    verify: (values) => {
      const read = readMail(textsOf(values), words.port, words.required);
      return isProblem(read) ? read.problem : undefined;
    },
    writes: {
      setMailSettings: async (values, { reason, key }) => {
        const settings = readMail(textsOf(values), words.port, words.required);
        if (isProblem(settings)) throw new Error(settings.problem);
        const sent = await unwrapChange(
          adminApi.PUT("/api/admin/v1/settings/mail", {
            params: { header: { "Idempotency-Key": key } },
            body: { settings, reason },
          }),
        );
        if (mailSavedRestart(sent.data)) say(t("settings.mail.restartSaved"));
        return sent;
      },
    },
  });
}

function Rows({ view }: { view: Settings }) {
  const { t } = useTranslation();
  const drafts = useMailDrafts(view);
  const [testing, setTesting] = useState(false);
  const test = () => {
    if (drafts.changed.length) say(t("settings.mail.testFirst"), { kind: "bad" });
    else setTesting(true);
  };
  return (
    <>
      <PageHead
        title={t("shell.nav.mail")}
        lede={t("settings.pages.mail.lede")}
        actions={
          <ActionButton icon="mail" onClick={test}>
            {t("settings.mail.test")}
          </ActionButton>
        }
      />
      <PageBody>
        <SettingsCard
          scope="platform"
          title={t("settings.pages.mail.service")}
          lede={t("settings.pages.mail.serviceLede")}
        >
          <SwitchSetting drafts={drafts} setting="mail.enabled" />
          <TextSetting drafts={drafts} setting="mail.host" width={240} mono />
          <NumberSetting drafts={drafts} setting="mail.port" min={1} max={65_535} />
          <TextSetting drafts={drafts} setting="mail.username" width={240} />
          <SecretSetting drafts={drafts} setting="mail.password" isSet={view.has_password === true} />
          <TextSetting drafts={drafts} setting="mail.from" width={280} />
          <Origin view={view} />
        </SettingsCard>
        <SettingsSubmit drafts={drafts} />
      </PageBody>
      {testing ? <MailTestDialog onClose={() => setTesting(false)} /> : null}
    </>
  );
}

// Where the settings in force come from, and that a saved change waits for a restart.
function Origin({ view }: { view: Settings }) {
  const { t } = useTranslation();
  return (
    <SettingRow
      title={t("settings.mail.source")}
      detail={view.restart_required ? <Text tone="warn">{t("settings.mail.restartBody")}</Text> : undefined}
    >
      <Tag>{t(`settings.mail.sources.${view.source ?? "environment"}`)}</Tag>
    </SettingRow>
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

export { default as messages } from "../locales/settings";
