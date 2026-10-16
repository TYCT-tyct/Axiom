import type { Schemas } from "@axiom/api";
import {
  Button,
  CheckField,
  FormError,
  moneyInput,
  parseMoney,
  SelectField,
  SubmitButton,
  TextAreaField,
  TextField,
} from "@axiom/ui";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useErrors } from "./errors";
import { readForm } from "./forms";

// What the form collects. A key is made with an expiry and changed with an on/off switch; the contract's two requests
// share the rest.
export type KeyValues = {
  name: string;
  disabled: boolean;
  expires_at?: string;
  spend_limit?: Schemas["Money"];
  limit_reset?: Schemas["LimitReset"];
  models?: string[];
  ip_allow?: string[];
};

const resets = ["daily", "weekly", "monthly"] as const;

// One entry per line; nothing typed means no restriction, which the contract spells as leaving the list out.
function lines(text: string | undefined) {
  const entries = (text ?? "")
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
  return entries.length > 0 ? entries : undefined;
}

type Invalid = "name" | "limit" | null;

// The spend limit field: nothing typed is no limit, and an amount needs the currency it is in.
function readLimit(values: Record<string, string>, currency: string | undefined) {
  const text = (values.spend_limit ?? "").trim();
  const amount = text ? parseMoney(text) : undefined;
  if (amount === undefined) {
    return undefined;
  }
  return amount === null || !currency ? null : { amount, currency };
}

function readValues(form: HTMLFormElement, currency: string | undefined): KeyValues | "name" | "limit" {
  const values = readForm(form);
  const name = (values.name ?? "").trim();
  const limit = readLimit(values, currency);
  if (!name || limit === null) {
    return name ? "limit" : "name";
  }
  return {
    name,
    disabled: values.disabled === "on",
    expires_at: values.expires_at ? new Date(values.expires_at).toISOString() : undefined,
    spend_limit: limit,
    limit_reset: limit && values.limit_reset ? (values.limit_reset as Schemas["LimitReset"]) : undefined,
    models: lines(values.models),
    ip_allow: lines(values.ip_allow),
  };
}

type Section = { apiKey?: Schemas["Key"]; error: unknown };

function Limit({ apiKey, error, currency, invalid }: Section & { currency: string | undefined; invalid: Invalid }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const unit = apiKey?.spend_limit?.currency ?? currency;
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <TextField
        label={t("keys.form.limit", { currency: unit ?? "" })}
        hint={apiKey ? t("keys.form.limitHintEdit") : t("keys.form.limitHintCreate")}
        name="spend_limit"
        inputMode="decimal"
        autoComplete="off"
        disabled={!unit}
        defaultValue={apiKey?.spend_limit ? moneyInput(apiKey.spend_limit.amount) : ""}
        error={invalid === "limit" ? t("keys.form.limitInvalid") : errors.field(error, "spend_limit")}
      />
      <SelectField label={t("keys.form.reset")} name="limit_reset" defaultValue={apiKey?.limit_reset ?? ""}>
        <option value="">{t("keys.form.resetNever")}</option>
        {resets.map((reset) => (
          <option key={reset} value={reset}>
            {t(`keys.reset.${reset}`)}
          </option>
        ))}
      </SelectField>
    </div>
  );
}

function Restrictions({ apiKey, error }: Section) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <TextAreaField
        label={t("keys.form.models")}
        hint={t("keys.form.modelsHint")}
        name="models"
        rows={3}
        spellCheck={false}
        defaultValue={apiKey?.models?.join("\n")}
        error={errors.field(error, "models")}
      />
      <TextAreaField
        label={t("keys.form.ips")}
        hint={t("keys.form.ipsHint")}
        name="ip_allow"
        rows={2}
        spellCheck={false}
        defaultValue={apiKey?.ip_allow?.join("\n")}
        error={errors.field(error, "ip_allow")}
      />
    </>
  );
}

// KeyForm is the settings of a key, for making one (no key) and for changing one (filled with its current settings,
// because the contract replaces them all). The expiry is asked once, when the key is made.
export function KeyForm({
  apiKey,
  currency,
  pending,
  error,
  onSubmit,
  onCancel,
}: Section & {
  currency: string | undefined;
  pending: boolean;
  onSubmit: (values: KeyValues) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [invalid, setInvalid] = useState<Invalid>(null);
  return (
    <form
      noValidate
      className="space-y-4"
      onSubmit={(event) => {
        event.preventDefault();
        const values = readValues(event.currentTarget, apiKey?.spend_limit?.currency ?? currency);
        setInvalid(typeof values === "object" ? null : values);
        if (typeof values === "object" && !pending) {
          onSubmit(values);
        }
      }}
    >
      {error ? <FormError>{errors.message(error)}</FormError> : null}
      <TextField
        label={t("keys.form.name")}
        name="name"
        maxLength={100}
        defaultValue={apiKey?.name}
        autoComplete="off"
        error={invalid === "name" ? t("keys.form.nameMissing") : errors.field(error, "name")}
      />
      <Limit apiKey={apiKey} error={error} currency={currency} invalid={invalid} />
      <Restrictions apiKey={apiKey} error={error} />
      {apiKey ? (
        <CheckField
          label={t("keys.form.disable")}
          hint={t("keys.form.disableHint")}
          name="disabled"
          defaultChecked={apiKey.disabled}
        />
      ) : (
        <TextField
          label={t("keys.form.expires")}
          hint={t("keys.form.expiresHint")}
          name="expires_at"
          type="datetime-local"
          error={errors.field(error, "expires_at")}
        />
      )}
      <div className="flex justify-end gap-2 pt-2">
        <Button size="md" onClick={onCancel}>
          {t("cancel")}
        </Button>
        <SubmitButton pending={pending}>{apiKey ? t("keys.form.save") : t("keys.form.create")}</SubmitButton>
      </div>
    </form>
  );
}
