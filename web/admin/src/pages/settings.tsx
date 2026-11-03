import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors, useSession } from "@axiom/app";
import {
  Button,
  CheckField,
  DataState,
  Icon,
  moneyInput,
  PageHeader,
  parseMoney,
  SelectField,
  TextField,
  useFormat,
} from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "../change";
import { useDuration } from "../durations";
import { FileParserSettings } from "../file-parser-settings";
import { KeyLimitsCard, KeyLimitsUnavailable } from "../key-limits";
import { MailSettings } from "../mail-settings";
import { modelPricesChangeUnconfirmed } from "../model-prices";
import { Facts, PageBody, State } from "../parts";
import { PaymentsSettings } from "../payments-settings";
import { ProbeSettings } from "../probe-settings";
import { accountQuery, siteKeyLimitsQuery, siteSettingsQuery } from "../queries";
import { SiteRegistration } from "../registration";
import { SearchSettings } from "../search-settings";
import { SegmentSettings } from "../segment-settings";
import { SiteSignIn } from "../sign-in";
import { SiteInformation } from "../site-info";
import { SiteLogo } from "../site-logo";

type Settings = AdminSchemas["SiteSettings"];

// The sessions and keys a site gives its users, each with the bounds the contract states. A value left empty is the
// default in code, so a site records only what it chooses differently.
const durations = [
  { name: "console_idle_timeout_seconds", min: 300, max: 2_592_000 },
  { name: "console_max_age_seconds", min: 300, max: 7_776_000 },
  { name: "admin_idle_timeout_seconds", min: 300, max: 43_200 },
  { name: "admin_max_age_seconds", min: 300, max: 86_400 },
] as const;

// The site the settings are of: the admin has no list of sites in M1, so it is the site the admin's own account is of.
export default function SettingsPage() {
  const { t } = useTranslation();
  const owner = useQuery(accountQuery(useSession().account));
  return (
    <>
      <PageHeader title={t("nav.settings")} description={t("settings.description")} />
      <PageBody>
        <DataState query={owner}>
          {(account) => (
            <>
              <SiteInformation siteId={account.site_id} />
              <SiteLogo siteId={account.site_id} />
              <SiteSettings siteId={account.site_id} />
              <SiteKeyLimits siteId={account.site_id} />
              <SiteRegistration siteId={account.site_id} />
              <SiteSignIn siteId={account.site_id} />
              <MailSettings />
              <SearchSettings />
              <FileParserSettings />
              <SegmentSettings />
              <ProbeSettings />
              <PaymentsSettings siteId={account.site_id} currency={account.balance.balance.currency} />
            </>
          )}
        </DataState>
      </PageBody>
    </>
  );
}

function SiteSettings({ siteId }: { siteId: AdminSchemas["SiteId"] }) {
  const settings = useQuery(siteSettingsQuery(siteId));
  return <DataState query={settings}>{(view) => <Current siteId={siteId} view={view} />}</DataState>;
}

// The limits on the keys of an account are the site's own, kept apart from the settings above: saving those replaces
// them whole, and a page opened before these limits existed would take them along.
function SiteKeyLimits({ siteId }: { siteId: AdminSchemas["SiteId"] }) {
  const limits = useQuery(siteKeyLimitsQuery(siteId));
  // A node of the release before has no such operation: the settings above still work.
  if (limits.isError) {
    return <KeyLimitsUnavailable level="site" />;
  }
  return (
    <DataState query={limits}>
      {(view) => (
        <KeyLimitsCard
          operation={`key-limits:${siteId}`}
          level="site"
          view={view}
          editable
          send={(body, { reason, key }) =>
            unwrapChange(
              adminApi.PUT("/api/admin/v1/sites/{site_id}/key-limits", {
                params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
                body: { limits: body, reason },
              }),
            )
          }
        />
      )}
    </DataState>
  );
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
  const preference = settings.billing_preference ?? defaults.billing_preference ?? "subscription_first";
  rows.push([
    t("settings.field.billing_preference"),
    <span key="preference">
      {t(`settings.billing.values.${preference}`)} {settings.billing_preference ? own : standard}
    </span>,
  ]);
  // A node of the release before this setting has no default for it: the row and the field stay off rather than send a
  // field that node would silently ignore, which would make the save look applied.
  if (defaults.model_prices_public !== undefined) {
    const publicPrices = settings.model_prices_public ?? defaults.model_prices_public;
    rows.push([
      t("settings.field.model_prices_public"),
      <span key="model-prices-public">
        {t(`settings.publicPrices.values.${publicPrices ? "public" : "private"}`)}{" "}
        {settings.model_prices_public === undefined ? standard : own}
      </span>,
    ]);
  }
  return (
    <>
      <Facts
        title={t("settings.title")}
        action={
          <Button size="md" onClick={() => setOpen(true)}>
            <Icon name="pencil" />
            {t("settings.edit")}
          </Button>
        }
        rows={rows}
      />
      {open ? <EditDialog siteId={siteId} view={view} onClose={() => setOpen(false)} /> : null}
    </>
  );
}

// The settings as typed: a value left empty is left out (the default applies), and one outside its bounds is a problem.
function readDurations(values: Record<string, string>, problem: string): Settings | Problem {
  const settings: Record<string, number> = {};
  for (const { name, min, max } of durations) {
    const typed = (values[name] ?? "").trim();
    if (typed === "") {
      continue;
    }
    const seconds = Number(typed);
    if (!/^\d+$/.test(typed) || seconds < min || seconds > max) {
      return { problem };
    }
    settings[name] = seconds;
  }
  return settings as Settings;
}

function readSettings(
  values: Record<string, string>,
  limitCurrency: string | undefined,
  words: { range: string; limit: string },
  supportsModelPrices: boolean,
): Settings | Problem {
  const settings = readDurations(values, words.range);
  if ("problem" in settings) {
    return settings;
  }
  const preference = (values.billing_preference ?? "").trim();
  if (preference !== "") {
    settings.billing_preference = preference as Settings["billing_preference"];
  }
  if (supportsModelPrices) {
    // The checkbox is only redrawn by this page when the node knows the field; unchecked means the site keeps its
    // prices to signed-in members, which is an explicit false rather than an omitted field.
    settings.model_prices_public = values.model_prices_public === "true";
  }
  const limit = (values.new_key_daily_limit ?? "").trim();
  if (limit !== "") {
    const amount = parseMoney(limit);
    if (amount === null || !limitCurrency || BigInt(amount) > 1_000_000_000_000_000n) {
      return { problem: words.limit };
    }
    settings.new_key_daily_limit = { amount, currency: limitCurrency };
  }
  return settings;
}

function SettingsFields({
  view,
  currency,
  error,
}: {
  view: AdminSchemas["SiteSettingsView"];
  currency: string | undefined;
  error: unknown;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const duration = useDuration();
  return (
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
      <SelectField
        label={t("settings.field.billing_preference")}
        hint={t("settings.billing.hint")}
        name="billing_preference"
        defaultValue={view.settings.billing_preference ?? view.defaults.billing_preference ?? "subscription_first"}
      >
        <option value="subscription_first">{t("settings.billing.values.subscription_first")}</option>
        <option value="wallet_first">{t("settings.billing.values.wallet_first")}</option>
        <option value="subscription_only">{t("settings.billing.values.subscription_only")}</option>
        <option value="wallet_only">{t("settings.billing.values.wallet_only")}</option>
      </SelectField>
      {view.defaults.model_prices_public !== undefined ? (
        <CheckField
          label={t("settings.field.model_prices_public")}
          hint={t("settings.publicPrices.hint")}
          name="model_prices_public"
          value="true"
          defaultChecked={view.settings.model_prices_public ?? view.defaults.model_prices_public}
        />
      ) : null}
      <TextField
        label={t("settings.field.new_key_daily_limit")}
        hint={t("settings.limitHint", { currency: currency ?? "" })}
        name="new_key_daily_limit"
        inputMode="decimal"
        autoComplete="off"
        defaultValue={view.settings.new_key_daily_limit ? moneyInput(view.settings.new_key_daily_limit.amount) : ""}
        error={errors.field(error, "new_key_daily_limit")}
      />
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
  const currency = view.defaults.new_key_daily_limit?.currency;
  const words = { range: t("settings.invalidRange"), limit: t("settings.invalidLimit", { currency: currency ?? "" }) };
  return (
    <ChangeDialog
      operation={`site-settings:${siteId}`}
      title={t("settings.edit")}
      intro={<p>{t("settings.intro")}</p>}
      action={t("settings.save")}
      fields={(error) => <SettingsFields view={view} currency={currency} error={error} />}
      read={(values) => readSettings(values, currency, words, view.defaults.model_prices_public !== undefined)}
      send={async (settings, { reason, key }) => {
        const result = await unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/settings", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { settings, reason },
          }),
        );
        const unconfirmed = modelPricesChangeUnconfirmed(view, settings, result.data);
        return { ...result, data: { ...result.data, modelPricesSaved: !unconfirmed } };
      }}
      verdict
      done={(out) => (
        <p role="status" className="flex items-start gap-2 text-t1">
          <Icon
            name={out.modelPricesSaved ? "check" : "alert"}
            className={out.modelPricesSaved ? "mt-0.5 shrink-0 text-ok" : "mt-0.5 shrink-0 text-bad"}
          />
          <span>{t(out.modelPricesSaved ? "settings.saved" : "settings.publicPrices.notSaved")}</span>
        </p>
      )}
      onClose={onClose}
    />
  );
}
