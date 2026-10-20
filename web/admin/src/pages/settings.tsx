import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors, useSession } from "@axiom/app";
import { Button, DataState, Icon, moneyInput, PageHeader, parseMoney, TextField, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "../change";
import { Actions, Facts, PageBody, State } from "../parts";
import { accountQuery, siteSettingsQuery } from "../queries";

type Settings = AdminSchemas["SiteSettings"];

// The sessions and keys a site gives its users, each with the bounds the contract states. A value left empty is the
// default in code, so a site records only what it chooses differently.
const durations = [
  { name: "console_idle_timeout_seconds", min: 300, max: 2_592_000 },
  { name: "console_max_age_seconds", min: 300, max: 7_776_000 },
  { name: "admin_idle_timeout_seconds", min: 300, max: 43_200 },
  { name: "admin_max_age_seconds", min: 300, max: 86_400 },
] as const;

// The limits that are counts of keys rather than lengths of time: the call keys one account may hold, and the ones it
// may make in an hour.
const counts = [
  { name: "max_keys_per_account", min: 1, max: 10_000 },
  { name: "key_creations_per_hour", min: 1, max: 10_000 },
] as const;

// The site the settings are of: the admin has no list of sites in M1, so it is the site the admin's own account is of.
export default function SettingsPage() {
  const { t } = useTranslation();
  const owner = useQuery(accountQuery(useSession().account));
  return (
    <>
      <PageHeader title={t("nav.settings")} description={t("settings.description")} />
      <PageBody>
        <DataState query={owner}>{(account) => <SiteSettings siteId={account.site_id} />}</DataState>
      </PageBody>
    </>
  );
}

function SiteSettings({ siteId }: { siteId: AdminSchemas["SiteId"] }) {
  const settings = useQuery(siteSettingsQuery(siteId));
  return <DataState query={settings}>{(view) => <Current siteId={siteId} view={view} />}</DataState>;
}

// seconds as the largest units that hold it: 172800 as "2 days".
function useDuration() {
  const { i18n } = useTranslation();
  return (seconds: number) => {
    const units = [
      ["day", 86_400],
      ["hour", 3_600],
      ["minute", 60],
      ["second", 1],
    ] as const;
    let rest = seconds;
    const parts: string[] = [];
    for (const [unit, size] of units) {
      const count = Math.floor(rest / size);
      rest -= count * size;
      if (count > 0) {
        parts.push(new Intl.NumberFormat(i18n.language, { style: "unit", unit, unitDisplay: "long" }).format(count));
      }
    }
    return parts.join(" ");
  };
}

function Current({ siteId, view }: { siteId: AdminSchemas["SiteId"]; view: AdminSchemas["SiteSettingsView"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  const duration = useDuration();
  const [open, setOpen] = useState(false);
  const { settings, defaults } = view;
  const own = <State tone="neutral">{t("settings.own")}</State>;
  const standard = <State tone="neutral">{t("settings.default")}</State>;
  const rows = durations.map(({ name }): [string, ReactNode] => {
    const value = settings[name];
    const shown = value ?? defaults[name] ?? 0;
    return [
      t(`settings.field.${name}`),
      <span key={name}>
        {duration(shown)} <span className="text-t4">{t("settings.raw", { seconds: format.number(shown) })}</span>{" "}
        {value === undefined ? standard : own}
      </span>,
    ];
  });
  const limit = settings.new_key_daily_limit ?? defaults.new_key_daily_limit;
  rows.push([
    t("settings.field.new_key_daily_limit"),
    <span key="limit">
      {limit ? format.money(limit) : "—"} {settings.new_key_daily_limit ? own : standard}
    </span>,
  ]);
  for (const { name } of counts) {
    const value = settings[name];
    const shown = value ?? defaults[name] ?? 0;
    rows.push([
      t(`settings.field.${name}`),
      <span key={name}>
        {format.number(shown)} {value === undefined ? standard : own}
      </span>,
    ]);
  }
  return (
    <>
      <Actions>
        <Button variant="primary" size="md" onClick={() => setOpen(true)}>
          <Icon name="pencil" />
          {t("settings.edit")}
        </Button>
      </Actions>
      <Facts title={t("settings.title")} rows={rows} />
      {open ? <EditDialog siteId={siteId} view={view} onClose={() => setOpen(false)} /> : null}
    </>
  );
}

// A whole number typed for one of the counted settings: empty is left out, so the default applies, and anything but an
// integer in the setting's bounds is the problem to show.
function readNumbers(
  values: Record<string, string>,
  settings: Record<string, unknown>,
  numbers: readonly { name: string; min: number; max: number }[],
  problem: string,
): string | undefined {
  for (const { name, min, max } of numbers) {
    const typed = (values[name] ?? "").trim();
    if (typed === "") {
      continue;
    }
    const count = Number(typed);
    if (!/^\d+$/.test(typed) || count < min || count > max) {
      return problem;
    }
    settings[name] = count;
  }
  return undefined;
}

// The settings as typed: a value left empty is left out (the default applies), and one outside its bounds is a problem.
function readSettings(
  values: Record<string, string>,
  limitCurrency: string | undefined,
  words: { range: string; count: string; limit: string },
): Settings | Problem {
  const settings: Record<string, unknown> = {};
  const problem =
    readNumbers(values, settings, durations, words.range) ?? readNumbers(values, settings, counts, words.count);
  if (problem !== undefined) {
    return { problem };
  }
  const limit = (values.new_key_daily_limit ?? "").trim();
  if (limit !== "") {
    const amount = parseMoney(limit);
    if (amount === null || !limitCurrency || BigInt(amount) > 1_000_000_000_000_000n) {
      return { problem: words.limit };
    }
    settings.new_key_daily_limit = { amount, currency: limitCurrency };
  }
  return settings as Settings;
}

// The counted limits of the form: the call keys one account may hold, and the ones it may make in an hour.
function KeyLimits({ view, error }: { view: AdminSchemas["SiteSettingsView"]; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      {counts.map(({ name, min, max }) => (
        <TextField
          key={name}
          label={t(`settings.field.${name}`)}
          hint={t("settings.countHint", { min, max, default: view.defaults[name] ?? 0 })}
          name={name}
          inputMode="numeric"
          autoComplete="off"
          defaultValue={view.settings[name]}
          error={errors.field(error, name)}
        />
      ))}
    </>
  );
}

function EditDialog({
  siteId,
  view,
  onClose,
}: {
  siteId: AdminSchemas["SiteId"];
  view: AdminSchemas["SiteSettingsView"];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const duration = useDuration();
  const currency = view.defaults.new_key_daily_limit?.currency;
  const words = {
    range: t("settings.invalidRange"),
    count: t("settings.invalidCount"),
    limit: t("settings.invalidLimit", { currency: currency ?? "" }),
  };
  return (
    <ChangeDialog
      title={t("settings.edit")}
      intro={<p>{t("settings.intro")}</p>}
      action={t("settings.save")}
      fields={(error) => (
        <>
          {durations.map(({ name, min, max }) => (
            <TextField
              key={name}
              label={t(`settings.field.${name}`)}
              hint={t("settings.hint", {
                min: duration(min),
                max: duration(max),
                default: duration(view.defaults[name] ?? 0),
              })}
              name={name}
              inputMode="numeric"
              autoComplete="off"
              defaultValue={view.settings[name]}
              error={errors.field(error, name)}
            />
          ))}
          <TextField
            label={t("settings.field.new_key_daily_limit")}
            hint={t("settings.limitHint", { currency: currency ?? "" })}
            name="new_key_daily_limit"
            inputMode="decimal"
            autoComplete="off"
            defaultValue={view.settings.new_key_daily_limit ? moneyInput(view.settings.new_key_daily_limit.amount) : ""}
            error={errors.field(error, "new_key_daily_limit")}
          />
          <KeyLimits view={view} error={error} />
        </>
      )}
      read={(values) => readSettings(values, currency, words)}
      send={(settings, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/settings", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { settings, reason },
          }),
        )
      }
      done={() => <p>{t("settings.saved")}</p>}
      onClose={onClose}
    />
  );
}
