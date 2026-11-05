import { type AdminSchemas, ApiError, adminApi, unwrapChange, unwrapEmptyChange } from "@axiom/api";
import { draftOf, type Invalid, RulesEditor, rulesOf, summaryOf, useClassOptions, useErrors } from "@axiom/app";
import { Button, DataState, Icon, SelectField, TextField } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, isProblem, type Problem, useSnapshot } from "./change";
import { Code, Facts } from "./parts";
import { layerDefaultQuery, type PresetLayer, sourceClassesQuery } from "./queries";

type Preset = AdminSchemas["RoutingPreset"];
type Default = AdminSchemas["RoutingDefault"];

export const systemPreset = "00000000-0000-0000-0000-000000000000";

const slugPattern = /^[a-z0-9][a-z0-9-]{0,63}$/;
const currencyPattern = /^[A-Z]{3}$/;

// The layer in an operation's name, so an unsettled try of one layer is never met again on another (change.tsx).
const layerName = (layer: PresetLayer) => (layer.scope === "platform" ? "platform" : layer.siteId);

// selectable tells whether a layer's default may point at the preset, as the server checks it: the platform's at a live
// platform preset that limits no price, since it serves every site, the system preset among them; a site's at a live
// preset of its own or of the platform that limits prices in its currency or none, never the system preset.
export function selectable(layer: PresetLayer, preset: Preset, currency: string | undefined) {
  if (preset.disabled) {
    return false;
  }
  if (layer.scope === "platform") {
    return preset.scope === "platform" && preset.currency === undefined;
  }
  if (preset.scope === "site") {
    return true;
  }
  return preset.id !== systemPreset && (preset.currency === undefined || preset.currency === currency);
}

// What the server says when a write is over a layer's limit or refuses the rules, in words for this page: the shared
// words for preset_quota speak of an account's presets, and a rules refusal names the rule in the server's own words.
function useExplain() {
  const { t } = useTranslation();
  return (error: unknown) => {
    if (!(error instanceof ApiError)) {
      return undefined;
    }
    if (error.code === "preset_quota") {
      return t("routingPresets.quota");
    }
    const detail = error.fields.find((field) => field.pointer === "/rules");
    return detail ? t("routingPresets.form.rulesRefused", { detail: detail.message }) : undefined;
  };
}

// DefaultCard shows the preset a layer's new keys take, and opens its change. Keys made before keep theirs.
export function DefaultCard({
  layer,
  presets,
  currency,
  manage,
}: {
  layer: PresetLayer;
  presets: Preset[];
  currency?: string;
  manage: boolean;
}) {
  const { t } = useTranslation();
  const query = useQuery(layerDefaultQuery(layer));
  const [open, setOpen] = useState(false);
  const title = t(`routingPresets.defaults.title.${layer.scope}`);
  const edit = manage ? (
    <Button size="md" onClick={() => setOpen(true)}>
      <Icon name="pencil" />
      {t("routingPresets.defaults.edit")}
    </Button>
  ) : undefined;
  return (
    <DataState query={query}>
      {(view) => (
        <>
          <Facts
            title={title}
            action={edit}
            rows={[
              [t("routingPresets.defaults.label"), <Chosen key="chosen" layer={layer} view={view} presets={presets} />],
            ]}
          />
          {open ? (
            <DefaultDialog
              layer={layer}
              view={view}
              options={presets.filter((preset) => selectable(layer, preset, currency))}
              onClose={() => setOpen(false)}
            />
          ) : null}
        </>
      )}
    </DataState>
  );
}

function Chosen({ layer, view, presets }: { layer: PresetLayer; view: Default; presets: Preset[] }) {
  const { t } = useTranslation();
  if (view.preset_id === undefined) {
    return t(`routingPresets.defaults.none.${layer.scope}`);
  }
  const preset = presets.find((candidate) => candidate.id === view.preset_id);
  return preset ? `${preset.name} (${preset.slug})` : <Code>{view.preset_id}</Code>;
}

function DefaultDialog({
  layer,
  view,
  options,
  onClose,
}: {
  layer: PresetLayer;
  view: Default;
  options: Preset[];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [read, reload] = useSnapshot(view);
  const chosen = read.preset_id ?? "";
  return (
    <ChangeDialog<string | null, Default>
      operation={`routing-default:${layerName(layer)}`}
      title={t(`routingPresets.defaults.title.${layer.scope}`)}
      intro={<p>{t("routingPresets.defaults.hint")}</p>}
      action={t("routingPresets.defaults.save")}
      reload={reload}
      fields={() => (
        <SelectField label={t("routingPresets.defaults.label")} name="preset_id" defaultValue={chosen}>
          <option value="">{t(`routingPresets.defaults.none.${layer.scope}`)}</option>
          {/* The default always points at a preset the layer may choose; one the list lacks is shown by its ID. */}
          {chosen === "" || options.some((preset) => preset.id === chosen) ? null : (
            <option value={chosen}>{chosen}</option>
          )}
          {options.map((preset) => (
            <option key={preset.id} value={preset.id}>
              {preset.name}
            </option>
          ))}
        </SelectField>
      )}
      read={(values) => values.preset_id || null}
      send={(presetId, { reason, key }) => {
        const body = { preset_id: presetId, version: read.version, reason };
        const header = { "Idempotency-Key": key };
        return unwrapChange(
          layer.scope === "platform"
            ? adminApi.PUT("/api/admin/v1/settings/routing", { params: { header }, body })
            : adminApi.PUT("/api/admin/v1/sites/{site_id}/routing-default", {
                params: { path: { site_id: layer.siteId }, header },
                body,
              }),
        );
      }}
      done={() => <p>{t("routingPresets.defaults.saved")}</p>}
      onClose={onClose}
    />
  );
}

type PresetInput = { slug: string; name: string; currency?: string; rules: AdminSchemas["RoutingRules"] };

// PresetDialog makes a preset of the layer, or changes one at the revision it was opened with: a change made elsewhere
// meanwhile is refused, and the dialog fills again from the preset as it is now. The rules are edited in place, so they
// live here, beside the form the change reads its name from.
export function PresetDialog({
  layer,
  preset,
  currency,
  onClose,
}: {
  layer: PresetLayer;
  preset?: Preset;
  currency?: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const explain = useExplain();
  const classes = useClassOptions(useQuery({ ...sourceClassesQuery, select: (view) => view.classes }));
  const [read, reload] = useSnapshot(preset);
  const [draft, setDraft] = useState(() => draftOf(read?.rules));
  const [invalid, setInvalid] = useState<Invalid | null>(null);
  const [code, setCode] = useState(read?.currency ?? currency ?? "");
  const refill = () => {
    reload();
    setDraft(draftOf(preset?.rules));
    setInvalid(null);
    setCode(preset?.currency ?? currency ?? "");
  };
  const readInput = (values: Record<string, string>): PresetInput | Problem => {
    const names = readNames(values, read === undefined, t);
    if (isProblem(names)) {
      return names;
    }
    const written = rulesOf(draft);
    setInvalid("invalid" in written ? written.invalid : null);
    if ("invalid" in written) {
      showRefusal();
      return { problem: t("routingPresets.form.rulesInvalid") };
    }
    const currency = currencyOf(layer, read, code.trim(), summaryOf(written.rules).priced);
    return typeof currency === "object"
      ? { problem: t(currency.problem) }
      : { ...names, rules: written.rules, currency };
  };
  return (
    <ChangeDialog<PresetInput, Preset>
      operation={read ? `routing-preset:${read.id}` : `routing-preset:new:${layerName(layer)}`}
      title={read ? t("routingPresets.editTitle", { name: read.name }) : t(`routingPresets.createTitle.${layer.scope}`)}
      action={read ? t("routingPresets.form.save") : t("routingPresets.form.create")}
      reload={read ? refill : undefined}
      explain={explain}
      fields={(error) => (
        <>
          <Names preset={read} error={error} />
          {layer.scope === "platform" ? <CurrencyField preset={read} code={code} onCode={setCode} /> : null}
          <RulesEditor
            draft={draft}
            invalid={invalid}
            currency={layer.scope === "platform" ? code : (currency ?? "")}
            classes={classes}
            onChange={setDraft}
          />
        </>
      )}
      read={readInput}
      send={(input, { reason, key }) => savePreset(layer, read, input, reason, key)}
      done={(saved) => <p>{t("routingPresets.saved", { name: saved.name })}</p>}
      onClose={onClose}
    />
  );
}

function readNames(
  values: Record<string, string>,
  made: boolean,
  t: TFunction<"admin">,
): Problem | { slug: string; name: string } {
  const name = (values.name ?? "").trim();
  const slug = values.slug ?? "";
  if (made && !slugPattern.test(slug)) {
    return { problem: t("routingPresets.form.slugInvalid") };
  }
  return name === "" ? { problem: t("routingPresets.form.nameMissing") } : { slug, name };
}

// currencyOf is the currency a write names, or the words for why it cannot. A site's preset names none: it is in its
// site's. A platform preset serves the sites of its currency, or every site when it has none, and a change never makes
// it serve more or fewer (internal/routing ErrPresetCurrency, visibleTo): a changed one keeps the currency it has, priced
// or not, so a rename never widens it, and one without a currency cannot gain a price. A new one names a currency only
// when it limits a price.
export function currencyOf(
  layer: PresetLayer,
  preset: Preset | undefined,
  code: string,
  priced: boolean,
): string | undefined | { problem: "routingPresets.form.cannotPrice" | "routingPresets.form.currencyInvalid" } {
  if (layer.scope === "site") {
    return undefined;
  }
  if (preset) {
    return preset.currency ?? (priced ? { problem: "routingPresets.form.cannotPrice" } : undefined);
  }
  if (!priced) {
    return undefined;
  }
  return currencyPattern.test(code) ? code : { problem: "routingPresets.form.currencyInvalid" };
}

// A refusal may sit far down the rules, away from the button pressed: once it renders, it is scrolled into view.
function showRefusal() {
  requestAnimationFrame(() =>
    document.querySelector("[role=dialog] [data-invalid]")?.scrollIntoView({ block: "center" }),
  );
}

function Names({ preset, error }: { preset?: Preset; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <TextField
        label={t("routingPresets.form.name")}
        name="name"
        defaultValue={preset?.name}
        maxLength={100}
        autoComplete="off"
        error={errors.field(error, "name")}
      />
      {preset ? (
        <div className="space-y-1">
          <p className="text-label text-t3">{t("routingPresets.form.slug")}</p>
          <Code>{preset.slug}</Code>
        </div>
      ) : (
        <TextField
          label={t("routingPresets.form.slug")}
          hint={t("routingPresets.form.slugHint")}
          name="slug"
          maxLength={64}
          autoComplete="off"
          spellCheck={false}
          error={errors.field(error, "slug")}
        />
      )}
    </div>
  );
}

// CurrencyField is the currency of a platform preset's price limits: chosen when it is made, fixed after.
function CurrencyField({ preset, code, onCode }: { preset?: Preset; code: string; onCode: (code: string) => void }) {
  const { t } = useTranslation();
  if (preset) {
    return (
      <p className="text-ui text-t2">
        {preset.currency
          ? t("routingPresets.form.currencyFixed", { currency: preset.currency })
          : t("routingPresets.form.unpriced")}
      </p>
    );
  }
  return (
    <TextField
      className="sm:w-48"
      label={t("routingPresets.form.currency")}
      hint={t("routingPresets.form.currencyHint")}
      value={code}
      maxLength={3}
      autoComplete="off"
      spellCheck={false}
      onChange={(event) => onCode(event.currentTarget.value.toUpperCase())}
    />
  );
}

function savePreset(layer: PresetLayer, preset: Preset | undefined, input: PresetInput, reason: string, key: string) {
  const header = { "Idempotency-Key": key };
  const { slug, ...content } = input;
  if (preset) {
    return unwrapChange(
      adminApi.PUT("/api/admin/v1/routing-presets/{preset_id}", {
        params: { path: { preset_id: preset.id }, header },
        body: { ...content, revision: preset.revision, reason },
      }),
    );
  }
  const body = { ...content, slug, reason };
  return unwrapChange(
    layer.scope === "platform"
      ? adminApi.POST("/api/admin/v1/routing-presets", { params: { header }, body })
      : adminApi.POST("/api/admin/v1/sites/{site_id}/routing-presets", {
          params: { path: { site_id: layer.siteId }, header },
          body,
        }),
  );
}

// DisablePresetDialog disables a preset at the revision it was opened with. A default that still points at it refuses
// (preset_in_use), whose words say to change the default first.
export function DisablePresetDialog({ preset, onClose }: { preset: Preset; onClose: () => void }) {
  const { t } = useTranslation();
  const [read, reload] = useSnapshot(preset);
  return (
    <ChangeDialog<number, null>
      operation={`routing-preset-disable:${preset.id}`}
      title={t("routingPresets.disableTitle", { name: read.name })}
      intro={<p>{t("routingPresets.disableBody")}</p>}
      action={t("routingPresets.disable")}
      danger
      reload={reload}
      read={() => read.revision}
      send={(revision, { reason, key }) =>
        unwrapEmptyChange(
          adminApi.DELETE("/api/admin/v1/routing-presets/{preset_id}", {
            params: { path: { preset_id: preset.id }, header: { "Idempotency-Key": key } },
            body: { revision, reason },
          }),
        )
      }
      done={() => <p>{t("routingPresets.disabledDone", { name: read.name })}</p>}
      onClose={onClose}
    />
  );
}
