import { ApiError } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { ActionButton, DataState, Field } from "@axiom/ui";
import type { UseQueryResult } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "./change";
import { Facts, State } from "./parts";

type Sent<T> = { data: T; replayed: boolean };
type ValueView = { settings: object; defaults: object };

// ValueField is one number of a settings record: where it lives in the API, what it is called, and how it reads.
export type ValueField = {
  key: string;
  label: string;
  hint: string;
  min: number;
  max: number;
  format: (value: number) => string;
};

// ValueForm is what the card and its dialog need to show and change a record: the words, the fields, and the write.
type ValueForm<T extends ValueView> = {
  operation: string;
  title: string;
  edit: string;
  intro: string;
  save: string;
  saved: string;
  invalid: string;
  fields: ValueField[];
  send: (settings: Record<string, number | undefined>, reason: string, key: string) => Promise<Sent<T>>;
};

// ValueSettings is a settings card whose values are all numbers. The view says what is set and what the defaults are;
// the dialog writes only the values the person typed, so an empty field goes back to the default in code.
export function ValueSettings<T extends ValueView>({
  query,
  unavailableLabel,
  unavailable,
  ...form
}: ValueForm<T> & { query: UseQueryResult<T>; unavailableLabel: string; unavailable: string }) {
  if (query.error instanceof ApiError && query.error.status === 404) {
    return <Facts title={form.title} rows={[[unavailableLabel, unavailable]]} />;
  }
  return <DataState query={query}>{(view) => <Values view={view} {...form} />}</DataState>;
}

function Values<T extends ValueView>({ view, ...form }: ValueForm<T> & { view: T }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const { title, edit, fields } = form;
  const settings = view.settings as Record<string, number | undefined>;
  const defaults = view.defaults as Record<string, number | undefined>;
  const own = <State tone="neutral">{t("settings.setHere")}</State>;
  const standard = <State tone="neutral">{t("settings.default")}</State>;
  const rows = fields.map((field): [string, ReactNode] => {
    const value = settings[field.key];
    const shown = value ?? defaults[field.key] ?? 0;
    return [
      field.label,
      <span key={field.key}>
        {field.format(shown)} {value === undefined ? standard : own}
      </span>,
    ];
  });
  return (
    <>
      <Facts
        title={title}
        actions={
          <ActionButton onClick={() => setOpen(true)} icon="pencil">
            {edit}
          </ActionButton>
        }
        rows={rows}
      />
      {open ? <EditDialog view={view} {...form} onClose={() => setOpen(false)} /> : null}
    </>
  );
}

function EditDialog<T extends ValueView>({
  view,
  onClose,
  operation,
  edit,
  intro,
  save,
  saved,
  invalid,
  fields,
  send,
}: ValueForm<T> & { view: T; onClose: () => void }) {
  const settings = view.settings as Record<string, number | undefined>;
  const read = (values: Record<string, string>) => readValues(fields, values, invalid);
  return (
    <ChangeDialog
      operation={operation}
      title={edit}
      intro={<p>{intro}</p>}
      action={save}
      fields={(error) => <Fields fields={fields} settings={settings} error={error} />}
      read={read}
      send={(values, { reason, key }) => send(values, reason, key)}
      done={() => <p>{saved}</p>}
      onClose={onClose}
    />
  );
}

function Fields({
  fields,
  settings,
  error,
}: {
  fields: ValueField[];
  settings: Record<string, number | undefined>;
  error: unknown;
}) {
  const errors = useErrors();
  return (
    <>
      {fields.map((field) => (
        <Field
          key={field.key}
          name={field.key}
          label={field.label}
          hint={field.hint}
          inputMode="numeric"
          autoComplete="off"
          defaultValue={settings[field.key]?.toString() ?? ""}
          error={errors.field(error, `settings/${field.key}`)}
        />
      ))}
    </>
  );
}

// readValues turns the dialog's text into the record to send: an empty field is absent, so the server falls back to the
// default in code, and anything outside a field's bounds is refused before the request.
export function readValues(
  fields: ValueField[],
  values: Record<string, string>,
  invalid: string,
): Record<string, number | undefined> | Problem {
  const out: Record<string, number | undefined> = {};
  for (const field of fields) {
    const raw = (values[field.key] ?? "").trim();
    if (raw === "") {
      out[field.key] = undefined;
      continue;
    }
    const value = Number(raw);
    if (!Number.isInteger(value) || value < field.min || value > field.max) {
      return { problem: invalid };
    }
    out[field.key] = value;
  }
  return out;
}
