import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { TextField } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "./change";
import { Facts, State } from "./parts";
import { searchSettingsQuery } from "./queries";
import { SettingsPage } from "./settings-page";

type Settings = AdminSchemas["SearchSettingsUpdate"]["settings"];

const optional = (text: string) => (text === "" ? undefined : text);

function readSearch(values: Record<string, string>, keyWithoutURL: string): Settings | Problem {
  const url = (values.url ?? "").trim();
  const key = (values.key ?? "").trim();
  if (url === "" && key !== "") {
    return { problem: keyWithoutURL };
  }
  return { url: optional(url), key: optional(key) };
}

export function SearchSettings() {
  const query = useQuery(searchSettingsQuery);
  const { t } = useTranslation();
  return (
    <SettingsPage
      query={query}
      labels={{
        title: t("settings.search.title"),
        unavailableLabel: t("settings.search.unavailableLabel"),
        unavailable: t("settings.search.unavailable"),
        edit: t("settings.search.edit"),
        test: t("settings.search.test"),
      }}
      facts={SearchFacts}
      dialogs={{ edit: SearchEditDialog, test: SearchTestDialog }}
    />
  );
}

function SearchFacts({ view, action }: { view: AdminSchemas["SearchSettings"]; action: ReactNode }) {
  const { t } = useTranslation();
  const configured = Boolean(view.url);
  const source = view.source === "database" ? "database" : "environment";
  const rows: [string, ReactNode][] = [
    [
      t("settings.search.state"),
      <State key="state" tone={configured ? "good" : "neutral"}>
        {t(configured ? "settings.search.on" : "settings.search.off")}
      </State>,
    ],
    [t("settings.search.url"), view.url || "—"],
    [t("settings.search.key"), t(view.has_key ? "settings.search.keySet" : "settings.search.keyMissing")],
    [t("settings.search.source"), t(`settings.search.sources.${source}`)],
  ];
  if (view.restart_required) {
    rows.push([t("settings.search.restart"), t("settings.search.restartBody")]);
  }
  return <Facts title={t("settings.search.title")} rows={rows} action={action} />;
}

function SearchEditDialog({ view, onClose }: { view: AdminSchemas["SearchSettings"]; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation="search-settings"
      title={t("settings.search.edit")}
      intro={<p>{t("settings.search.intro")}</p>}
      action={t("settings.search.save")}
      fields={(error) => <SearchFields view={view} error={error} />}
      read={(values) => readSearch(values, t("settings.search.keyWithoutURL"))}
      send={(settings, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/settings/search", {
            params: { header: { "Idempotency-Key": key } },
            body: { settings, reason },
          }),
        )
      }
      done={(saved) => <SearchSaved saved={saved} />}
      onClose={onClose}
    />
  );
}

function SearchFields({ view, error }: { view: AdminSchemas["SearchSettings"]; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <TextField
        label={t("settings.search.url")}
        hint={t("settings.search.urlHint")}
        name="url"
        inputMode="url"
        autoComplete="off"
        defaultValue={view.url ?? ""}
        error={errors.field(error, "settings/url")}
      />
      <TextField
        label={t("settings.search.key")}
        hint={t("settings.search.keyHint")}
        name="key"
        type="password"
        autoComplete="new-password"
        error={errors.field(error, "settings")}
      />
    </>
  );
}

function SearchSaved({ saved }: { saved: AdminSchemas["SearchSettings"] }) {
  const { t } = useTranslation();
  return (
    <>
      <p>{t("settings.search.saved")}</p>
      {saved.restart_required === true ? <p>{t("settings.search.restartSaved")}</p> : null}
    </>
  );
}

function SearchTestDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation="search-settings-test"
      title={t("settings.search.testTitle")}
      intro={<p>{t("settings.search.testIntro")}</p>}
      action={t("settings.search.send")}
      verdict
      fields={() => null}
      read={() => ({})}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/settings/search/tests", {
            params: { header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      done={(result) => <p>{t(`settings.search.status.${result.status ?? "failed"}`)}</p>}
      onClose={onClose}
    />
  );
}
