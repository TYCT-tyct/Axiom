import type { AdminSchemas } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { TextField } from "@axiom/ui";
import type { UseQueryResult } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "./change";
import { Facts, State } from "./parts";
import { SettingsPage } from "./settings-page";

// BackendView is the admin API's answer for a backend set by a base URL and an optional key. The file parser is such a
// backend: the words come from its locale block, named by prefix, and the two calls from its own endpoints. Search has
// an engine list of its own and does not use this card.
type BackendView = {
  url?: string;
  has_key?: boolean;
  restart_required?: boolean;
  source?: string;
};

// BackendPrefix names the locale block of a backend. The file parser is the backend that is still one URL and key.
type BackendPrefix = "settings.fileParser";

type BackendUpdate = { url?: string; key?: string };

// Sent is what a change answers: the result and whether it is the stored result of an earlier try.
type Sent<T> = { data: T; replayed: boolean };

const optional = (text: string) => (text === "" ? undefined : text);

// readBackend keeps an empty key meaning "keep the saved one"; a key with no URL has no service to go to.
function readBackend(values: Record<string, string>, keyWithoutURL: string): BackendUpdate | Problem {
  const url = (values.url ?? "").trim();
  const key = (values.key ?? "").trim();
  if (url === "" && key !== "") {
    return { problem: keyWithoutURL };
  }
  return { url: optional(url), key: optional(key) };
}

// BackendSettings is one backend the admin configures: its state, the dialog that edits the URL and key, and the health
// test. A resource supplies the words, the audit operation and the two writes.
export function BackendSettings<T extends BackendView>({
  query,
  prefix,
  operation,
  save,
  test,
}: {
  query: UseQueryResult<T>;
  prefix: BackendPrefix;
  operation: string;
  save: (settings: BackendUpdate, reason: string, key: string) => Promise<Sent<T>>;
  test: (reason: string, key: string) => Promise<Sent<AdminSchemas["ConfigTestResult"]>>;
}) {
  const { t } = useTranslation();
  return (
    <SettingsPage
      query={query}
      labels={{
        title: t(`${prefix}.title`),
        unavailableLabel: t(`${prefix}.unavailableLabel`),
        unavailable: t(`${prefix}.unavailable`),
        edit: t(`${prefix}.edit`),
        test: t(`${prefix}.test`),
      }}
      facts={({ view, action }) => <BackendFacts prefix={prefix} view={view} action={action} />}
      dialogs={{
        edit: ({ view, onClose }) => (
          <BackendEditDialog prefix={prefix} operation={operation} save={save} view={view} onClose={onClose} />
        ),
        test: ({ onClose }) => (
          <BackendTestDialog prefix={prefix} operation={`${operation}-test`} test={test} onClose={onClose} />
        ),
      }}
    />
  );
}

function BackendFacts({ prefix, view, action }: { prefix: BackendPrefix; view: BackendView; action: ReactNode }) {
  const { t } = useTranslation();
  const configured = Boolean(view.url);
  const source = view.source === "database" ? "database" : "environment";
  const rows: [string, ReactNode][] = [
    [
      t(`${prefix}.state`),
      <State key="state" tone={configured ? "good" : "neutral"}>
        {t(`${prefix}.${configured ? "on" : "off"}`)}
      </State>,
    ],
    [t(`${prefix}.url`), view.url || "—"],
    [t(`${prefix}.key`), t(`${prefix}.${view.has_key ? "keySet" : "keyMissing"}`)],
    [t(`${prefix}.source`), t(`${prefix}.sources.${source}`)],
  ];
  if (view.restart_required) {
    rows.push([t(`${prefix}.restart`), t(`${prefix}.restartBody`)]);
  }
  return <Facts title={t(`${prefix}.title`)} rows={rows} action={action} />;
}

function BackendEditDialog<T extends BackendView>({
  prefix,
  operation,
  save,
  view,
  onClose,
}: {
  prefix: BackendPrefix;
  operation: string;
  save: (settings: BackendUpdate, reason: string, key: string) => Promise<Sent<T>>;
  view: T;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={operation}
      title={t(`${prefix}.edit`)}
      intro={<p>{t(`${prefix}.intro`)}</p>}
      action={t(`${prefix}.save`)}
      fields={(error) => <BackendFields prefix={prefix} view={view} error={error} />}
      read={(values) => readBackend(values, t(`${prefix}.keyWithoutURL`))}
      send={(settings, { reason, key }) => save(settings, reason, key)}
      done={(saved) => <BackendSaved prefix={prefix} saved={saved} />}
      onClose={onClose}
    />
  );
}

function BackendFields({ prefix, view, error }: { prefix: BackendPrefix; view: BackendView; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <TextField
        label={t(`${prefix}.url`)}
        hint={t(`${prefix}.urlHint`)}
        name="url"
        inputMode="url"
        autoComplete="off"
        defaultValue={view.url ?? ""}
        error={errors.field(error, "settings/url")}
      />
      <TextField
        label={t(`${prefix}.key`)}
        hint={t(`${prefix}.keyHint`)}
        name="key"
        type="password"
        autoComplete="new-password"
        error={errors.field(error, "settings")}
      />
    </>
  );
}

function BackendSaved({ prefix, saved }: { prefix: BackendPrefix; saved: BackendView }) {
  const { t } = useTranslation();
  return (
    <>
      <p>{t(`${prefix}.saved`)}</p>
      {saved.restart_required === true ? <p>{t(`${prefix}.restartSaved`)}</p> : null}
    </>
  );
}

function BackendTestDialog({
  prefix,
  operation,
  test,
  onClose,
}: {
  prefix: BackendPrefix;
  operation: string;
  test: (reason: string, key: string) => Promise<Sent<AdminSchemas["ConfigTestResult"]>>;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={operation}
      title={t(`${prefix}.testTitle`)}
      intro={<p>{t(`${prefix}.testIntro`)}</p>}
      action={t(`${prefix}.send`)}
      verdict
      fields={() => null}
      read={() => ({})}
      send={(_, { reason, key }) => test(reason, key)}
      done={(result) => <p>{t(`${prefix}.status.${result.status ?? "failed"}`)}</p>}
      onClose={onClose}
    />
  );
}
