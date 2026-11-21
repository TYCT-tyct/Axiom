import type { Schemas } from "@axiom/api";
import { readForm, systemPreset, useErrors } from "@axiom/app";
import {
  Button,
  CheckField,
  Cluster,
  FormError,
  Grid,
  moneyInput,
  parseMoney,
  SelectField,
  Stack,
  SubmitButton,
  Text,
  TextAreaField,
  TextField,
} from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { balanceQuery, workspacePresetQueries } from "./queries";
import { useWorkspaceChoice, useWorkspaces, WorkspaceField } from "./workspaces";

// What the form collects. A key is made with an expiry in a workspace and changed with an on/off switch; the contract's
// two requests share the rest.
export type KeyValues = {
  name: string;
  disabled: boolean;
  expires_at?: string;
  spend_limit?: Schemas["Money"];
  limit_reset?: Schemas["LimitReset"];
  models?: string[];
  ip_allow?: string[];
  preset_id?: string;
  workspace_id?: string;
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
    preset_id: values.preset_id || undefined,
    workspace_id: values.workspace_id || undefined,
  };
}

// ResetField is when a limit's count starts again, which a key's spend limit and a workspace's budget share.
export function ResetField({ value }: { value: Schemas["LimitReset"] | undefined }) {
  const { t } = useTranslation();
  return (
    <SelectField label={t("keys.form.reset")} name="limit_reset" defaultValue={value ?? ""}>
      <option value="">{t("keys.form.resetNever")}</option>
      {resets.map((reset) => (
        <option key={reset} value={reset}>
          {t(`keys.reset.${reset}`)}
        </option>
      ))}
    </SelectField>
  );
}

type Section = { apiKey?: Schemas["Key"]; error: unknown };

// The currency a limit is in is the account's, which the balance tells; a limit already set also carries it. Without
// one is where a limit is first set (or set again after it was cleared), so the field waits for the balance and says
// why when it cannot have it.
export function useCurrency(limit: Schemas["Money"] | undefined) {
  const balance = useQuery(balanceQuery);
  const unit = limit?.currency ?? balance.data?.balance.currency;
  return { unit, unavailable: !unit && balance.isError };
}

function Limit({
  apiKey,
  error,
  invalid,
  unit,
  unavailable,
}: Section & { invalid: Invalid; unit: string | undefined; unavailable: boolean }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const waiting = unavailable ? t("keys.form.limitCurrencyFailed") : t("keys.form.limitCurrencyLoading");
  const usual = apiKey ? t("keys.form.limitHintEdit") : t("keys.form.limitHintCreate");
  return (
    <Grid columns={2}>
      <TextField
        label={t("keys.form.limit", { currency: unit ?? "" })}
        hint={unit ? usual : waiting}
        name="spend_limit"
        inputMode="decimal"
        autoComplete="off"
        disabled={!unit}
        defaultValue={apiKey?.spend_limit ? moneyInput(apiKey.spend_limit.amount) : ""}
        error={invalid === "limit" ? t("keys.form.limitInvalid") : errors.field(error, "spend_limit")}
      />
      <ResetField value={apiKey?.limit_reset} />
    </Grid>
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
      {apiKey?.retired_models?.length ? (
        <Text size="sm" tone="warn">
          {t("keys.retiredModels", { models: apiKey.retired_models.join(", ") })}
        </Text>
      ) : null}
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

// PresetField chooses the key's routing preset among those a key of its workspace may use (scope; none is the default
// workspace), which differ by workspace: each has presets and a default of its own. A new key left at the first
// option takes its layers' default when it is made; a changed key keeps its preset unless another is chosen, so the
// form sends the field only for a choice. The choice is held here, not in the list: a later read of the list that fails
// or no longer has it keeps the choice and its option, so a submit never falls back to the default the user moved
// away from. Presets that could not be read at all leave the field out and the key keeps what it has. A management
// key has no preset.
function PresetField({ apiKey, error, scope }: Section & { scope: string | undefined }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const queries = workspacePresetQueries(scope);
  const presets = useQuery(queries.presets);
  const defaults = useQuery(queries.defaults);
  const newKeys = useNewKeys(scope);
  const presetName = usePresetName();
  const current = apiKey?.preset_id;
  const [chosen, setChosen] = useState(current ?? "");
  if (apiKey?.kind === "management") {
    return null;
  }
  if (!presets.data) {
    return presets.isError ? (
      <Text size="sm" tone="faint">
        {t("routingPresets.key.unavailable")}
      </Text>
    ) : null;
  }
  const list = presets.data.data;
  const named = (id: string | undefined) => list.find((preset) => preset.id === id)?.name;
  return (
    <SelectField
      label={t("routingPresets.key.field")}
      hint={t("routingPresets.key.hint")}
      name="preset_id"
      value={chosen}
      onChange={(event) => setChosen(event.currentTarget.value)}
      error={errors.field(error, "preset_id")}
    >
      {apiKey && !current ? <option value="">{t("routingPresets.key.legacy")}</option> : null}
      {apiKey ? null : <ByDefault newKeys={newKeys} accountDefault={named(defaults.data?.preset_id)} />}
      {chosen && !named(chosen) ? (
        <option value={chosen}>{presetName(chosen === current ? apiKey : undefined)}</option>
      ) : null}
      <PresetOptions presets={list} newKeys={newKeys.id} />
    </SelectField>
  );
}

// ByDefault is a new key's choice to take its layers' default: named by what the node says a key made now takes, or by
// the account's own default when an older node says nothing.
function ByDefault({ newKeys, accountDefault }: { newKeys: NewKeys; accountDefault: string | undefined }) {
  const { t } = useTranslation();
  const name = newKeys.id ? newKeys.name : accountDefault;
  if (newKeys.refused) {
    return <option value="">{t("routingPresets.key.byDefaultRefused")}</option>;
  }
  return (
    <option value="">{name ? t("routingPresets.key.byDefault", { name }) : t("routingPresets.key.byLayers")}</option>
  );
}

// PresetOptions offers presets by name and the layer they come from, as every preset choice shows them; the one a key
// made now takes is marked, so moving a key onto the current default is choosing it.
function PresetOptions({ presets, newKeys }: { presets: Schemas["RoutingPreset"][]; newKeys?: string }) {
  const { t } = useTranslation();
  return presets.map((preset) => (
    <option key={preset.id} value={preset.id}>
      {`${preset.name} · ${t(`routingPresets.layer.${preset.scope}`)}`}
      {preset.id === newKeys ? ` · ${t("routingPresets.key.current")}` : ""}
    </option>
  ));
}

type NewKeys = { id?: string; name?: string; refused: boolean; layer?: Schemas["RoutingPresetScope"] };

// useNewKeys is the preset a key made now in workspace scope (none: the default one) takes, as that workspace's default
// read says: its ID, and its name among the presets its keys may use (the system preset by its label; none for one the list lacks), or that the default
// refuses new keys, with the layer whose default decides. An older node says neither, and the page then says what it
// said before.
export function useNewKeys(scope?: string): NewKeys {
  const { t } = useTranslation();
  const queries = workspacePresetQueries(scope);
  const defaults = useQuery(queries.defaults).data;
  const presets = useQuery(queries.presets).data?.data;
  const id = defaults?.new_keys_preset_id;
  const layer = defaults?.new_keys_layer;
  if (!id) {
    return { refused: defaults?.new_keys_refused === true, layer };
  }
  const name = id === systemPreset ? t("routingPresets.key.system") : presets?.find((preset) => preset.id === id)?.name;
  return { id, name, refused: false, layer };
}

// usePresetName names a key's preset as the account can see it: its name, marked when disabled, or a placeholder for
// one it cannot read.
export function usePresetName() {
  const { t } = useTranslation();
  return (apiKey: Schemas["Key"] | undefined) => {
    const preset = apiKey?.preset;
    if (!preset) {
      return t("routingPresets.key.hidden");
    }
    const name = preset.id === systemPreset ? t("routingPresets.key.system") : preset.name;
    return preset.disabled ? t("routingPresets.key.disabledName", { name }) : name;
  };
}

// KeyForm is the settings of a key, for making one (no key) and for changing one (filled with its current settings,
// because the contract replaces them all). The expiry and the workspace are asked once, when the key is made; workspace
// is the one the form starts on. The presets offered are those of the key's workspace, and a change of workspace starts
// the preset choice again, since a preset of one workspace cannot be chosen in another.
export function KeyForm({
  apiKey,
  workspace,
  pending,
  error,
  onSubmit,
  onCancel,
}: Section & {
  workspace?: string;
  pending: boolean;
  onSubmit: (values: KeyValues) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [invalid, setInvalid] = useState<Invalid>(null);
  const currency = useCurrency(apiKey?.spend_limit);
  const choice = useWorkspaceChoice(workspace);
  const scope = useWorkspaces().scopeOf(apiKey?.workspace_id);
  const presetScope = apiKey ? scope : choice.scope;
  return (
    <form
      noValidate
      onSubmit={(event) => {
        event.preventDefault();
        const values = readValues(event.currentTarget, currency.unit);
        setInvalid(typeof values === "object" ? null : values);
        if (typeof values === "object" && !pending && (apiKey || !choice.lost)) {
          onSubmit(values);
        }
      }}
    >
      <Stack>
        {error ? <FormError>{errors.message(error)}</FormError> : null}
        <TextField
          label={t("keys.form.name")}
          name="name"
          maxLength={100}
          defaultValue={apiKey?.name}
          autoComplete="off"
          error={invalid === "name" ? t("keys.form.nameMissing") : errors.field(error, "name")}
        />
        {apiKey ? null : <WorkspaceField choice={choice} />}
        <Limit apiKey={apiKey} error={error} invalid={invalid} {...currency} />
        <PresetField key={presetScope} apiKey={apiKey} error={error} scope={presetScope} />
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
        <Cluster justify="end">
          <Button size="md" onClick={onCancel}>
            {t("cancel", { ns: "common" })}
          </Button>
          <SubmitButton pending={pending}>{apiKey ? t("keys.form.save") : t("keys.form.create")}</SubmitButton>
        </Cluster>
      </Stack>
    </form>
  );
}
