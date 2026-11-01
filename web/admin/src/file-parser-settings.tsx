import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { TextField } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "./change";
import { Facts, State } from "./parts";
import { fileParserSettingsQuery } from "./queries";
import { SettingsPage } from "./settings-page";

type Settings = AdminSchemas["FileParserSettingsUpdate"]["settings"];

const optional = (text: string) => (text === "" ? undefined : text);

function readFileParser(values: Record<string, string>, keyWithoutURL: string): Settings | Problem {
  const url = (values.url ?? "").trim();
  const key = (values.key ?? "").trim();
  if (url === "" && key !== "") {
    return { problem: keyWithoutURL };
  }
  return { url: optional(url), key: optional(key) };
}

export function FileParserSettings() {
  const query = useQuery(fileParserSettingsQuery);
  const { t } = useTranslation();
  return (
    <SettingsPage
      query={query}
      labels={{
        title: t("settings.fileParser.title"),
        unavailableLabel: t("settings.fileParser.unavailableLabel"),
        unavailable: t("settings.fileParser.unavailable"),
        edit: t("settings.fileParser.edit"),
        test: t("settings.fileParser.test"),
      }}
      facts={FileParserFacts}
      dialogs={{ edit: FileParserEditDialog, test: FileParserTestDialog }}
    />
  );
}

function FileParserFacts({ view, action }: { view: AdminSchemas["FileParserSettings"]; action: ReactNode }) {
  const { t } = useTranslation();
  const configured = Boolean(view.url);
  const source = view.source === "database" ? "database" : "environment";
  const rows: [string, ReactNode][] = [
    [
      t("settings.fileParser.state"),
      <State key="state" tone={configured ? "good" : "neutral"}>
        {t(configured ? "settings.fileParser.on" : "settings.fileParser.off")}
      </State>,
    ],
    [t("settings.fileParser.url"), view.url || "—"],
    [t("settings.fileParser.key"), t(view.has_key ? "settings.fileParser.keySet" : "settings.fileParser.keyMissing")],
    [t("settings.fileParser.source"), t(`settings.fileParser.sources.${source}`)],
  ];
  if (view.restart_required) {
    rows.push([t("settings.fileParser.restart"), t("settings.fileParser.restartBody")]);
  }
  return <Facts title={t("settings.fileParser.title")} rows={rows} action={action} />;
}

function FileParserEditDialog({ view, onClose }: { view: AdminSchemas["FileParserSettings"]; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation="file-parser-settings"
      title={t("settings.fileParser.edit")}
      intro={<p>{t("settings.fileParser.intro")}</p>}
      action={t("settings.fileParser.save")}
      fields={(error) => <FileParserFields view={view} error={error} />}
      read={(values) => readFileParser(values, t("settings.fileParser.keyWithoutURL"))}
      send={(settings, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/settings/file-parser", {
            params: { header: { "Idempotency-Key": key } },
            body: { settings, reason },
          }),
        )
      }
      done={(saved) => <FileParserSaved saved={saved} />}
      onClose={onClose}
    />
  );
}

function FileParserFields({ view, error }: { view: AdminSchemas["FileParserSettings"]; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <TextField
        label={t("settings.fileParser.url")}
        hint={t("settings.fileParser.urlHint")}
        name="url"
        inputMode="url"
        autoComplete="off"
        defaultValue={view.url ?? ""}
        error={errors.field(error, "settings/url")}
      />
      <TextField
        label={t("settings.fileParser.key")}
        hint={t("settings.fileParser.keyHint")}
        name="key"
        type="password"
        autoComplete="new-password"
        error={errors.field(error, "settings")}
      />
    </>
  );
}

function FileParserSaved({ saved }: { saved: AdminSchemas["FileParserSettings"] }) {
  const { t } = useTranslation();
  return (
    <>
      <p>{t("settings.fileParser.saved")}</p>
      {saved.restart_required === true ? <p>{t("settings.fileParser.restartSaved")}</p> : null}
    </>
  );
}

function FileParserTestDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation="file-parser-settings-test"
      title={t("settings.fileParser.testTitle")}
      intro={<p>{t("settings.fileParser.testIntro")}</p>}
      action={t("settings.fileParser.send")}
      verdict
      fields={() => null}
      read={() => ({})}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/settings/file-parser/tests", {
            params: { header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      done={(result) => <p>{t(`settings.fileParser.status.${result.status ?? "failed"}`)}</p>}
      onClose={onClose}
    />
  );
}
