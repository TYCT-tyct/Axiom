import { type AdminSchemas, ApiError, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Button, DataState, Icon, TextField } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { type ReactElement, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "./change";
import { Facts, State } from "./parts";
import { searchSettingsQuery } from "./queries";

type Settings = AdminSchemas["SearchSettingsUpdate"]["settings"];

// SearchUnavailable stands in for the card where a node of an older release has no search settings.
function SearchUnavailable() {
  const { t } = useTranslation();
  return (
    <Facts
      title={t("settings.search.title")}
      rows={[[t("settings.search.unavailableLabel"), t("settings.search.unavailable")]]}
    />
  );
}

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
  if (query.error instanceof ApiError && query.error.status === 404) {
    return <SearchUnavailable />;
  }
  return <DataState query={query}>{(view) => <Card view={view} />}</DataState>;
}

function Card({ view }: { view: AdminSchemas["SearchSettings"] }) {
  const { t } = useTranslation();
  const [edit, setEdit] = useState(false);
  const [test, setTest] = useState(false);
  return (
    <>
      <SearchFacts
        view={view}
        action={
          <div className="flex gap-2">
            <Button size="md" onClick={() => setTest(true)}>
              <Icon name="check" />
              {t("settings.search.test")}
            </Button>
            <Button size="md" onClick={() => setEdit(true)}>
              <Icon name="pencil" />
              {t("settings.search.edit")}
            </Button>
          </div>
        }
      />
      {edit ? <EditDialog view={view} onClose={() => setEdit(false)} /> : null}
      {test ? <TestDialog onClose={() => setTest(false)} /> : null}
    </>
  );
}

function SearchFacts({ view, action }: { view: AdminSchemas["SearchSettings"]; action: ReactElement }) {
  const { t } = useTranslation();
  const configured = Boolean(view.url);
  const source = view.source === "database" ? "database" : "environment";
  const rows: [string, ReactElement | string][] = [
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

function EditDialog({ view, onClose }: { view: AdminSchemas["SearchSettings"]; onClose: () => void }) {
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

function TestDialog({ onClose }: { onClose: () => void }) {
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
