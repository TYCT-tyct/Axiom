import type { AdminSchemas } from "@axiom/api";
import { CheckField, SelectField, TextField } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ModelField } from "../models/model-field";
import { RatesFields } from "../models/price-fields";
import { authorsQuery, tiersQuery } from "../queries";

export type Rule = AdminSchemas["PricingRuleInput"];
type Target = Rule["target"];
type Audience = Rule["audience"];
type Method = Rule["method"];

const targetKinds = [
  "all",
  "author",
  "model",
  "supplier",
  "group",
  "endpoint",
  "source_type",
] as const satisfies readonly Target["kind"][];
const audienceKinds = ["all", "tier", "account"] as const satisfies readonly Audience["kind"][];
const methodKinds = ["cost_plus", "reference_multiplier", "fixed"] as const satisfies readonly Method["kind"][];

// What a rule prices: the widest is everything, down to one endpoint. A target other than "all" names the thing by its
// ID, a model by its name, and a model author from the catalog's list.
function TargetFields({ target }: { target?: Target }) {
  const { t } = useTranslation();
  const authors = useQuery(authorsQuery);
  const [kind, setKind] = useState<Target["kind"]>(target?.kind ?? "all");
  return (
    <>
      <SelectField
        label={t("pricing.rule.target")}
        name="target_kind"
        value={kind}
        onChange={(event) => setKind(targetKinds.find((k) => k === event.target.value) ?? "all")}
      >
        {targetKinds.map((value) => (
          <option key={value} value={value}>
            {t(`pricing.target.${value}`)}
          </option>
        ))}
      </SelectField>
      {kind === "model" ? (
        <ModelField
          label={t("pricing.rule.targetModel")}
          currentId={target?.kind === "model" ? target.id : undefined}
        />
      ) : null}
      {kind === "author" ? (
        <SelectField label={t("pricing.rule.targetId")} name="target_author" defaultValue={target?.id ?? ""}>
          <option value="">{t("pricing.rule.choose")}</option>
          {authors.data?.data.map((author) => (
            <option key={author.slug} value={author.slug}>
              {author.name}
            </option>
          ))}
        </SelectField>
      ) : null}
      {kind === "supplier" || kind === "group" || kind === "endpoint" || kind === "source_type" ? (
        <TextField
          label={t("pricing.rule.targetId")}
          hint={t(`pricing.rule.targetHint.${kind}`)}
          name="target_id"
          defaultValue={target?.id}
          maxLength={200}
          autoComplete="off"
          spellCheck={false}
        />
      ) : null}
    </>
  );
}

// Whom a rule prices for: everyone, the users of one tier, or one account.
function AudienceFields({ audience }: { audience?: Audience }) {
  const { t } = useTranslation();
  const tiers = useQuery(tiersQuery);
  const [kind, setKind] = useState<Audience["kind"]>(audience?.kind ?? "all");
  return (
    <>
      <SelectField
        label={t("pricing.rule.audience")}
        name="audience_kind"
        value={kind}
        onChange={(event) => setKind(audienceKinds.find((k) => k === event.target.value) ?? "all")}
      >
        {audienceKinds.map((value) => (
          <option key={value} value={value}>
            {t(`pricing.audience.${value}`)}
          </option>
        ))}
      </SelectField>
      {kind === "tier" ? (
        <SelectField label={t("pricing.rule.tier")} name="audience_tier" defaultValue={audience?.id ?? ""}>
          <option value="">{t("pricing.rule.choose")}</option>
          {tiers.data?.data.map((tier) => (
            <option key={tier.id} value={tier.id}>
              {tier.name}
            </option>
          ))}
        </SelectField>
      ) : null}
      {kind === "account" ? (
        <TextField
          label={t("pricing.rule.account")}
          hint={t("pricing.rule.accountHint")}
          name="audience_account"
          defaultValue={audience?.id}
          maxLength={64}
          autoComplete="off"
          spellCheck={false}
        />
      ) : null}
    </>
  );
}

function CostPlusFields({ method }: { method?: Method }) {
  const { t } = useTranslation();
  const margin = method?.margin_ratio !== undefined;
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <SelectField label={t("pricing.method.basis")} name="basis" defaultValue={margin ? "margin" : "markup"}>
        <option value="markup">{t("pricing.method.markup")}</option>
        <option value="margin">{t("pricing.method.margin")}</option>
      </SelectField>
      <TextField
        label={t("pricing.method.ratio")}
        hint={t("pricing.method.ratioHint")}
        name="ratio"
        inputMode="decimal"
        autoComplete="off"
        defaultValue={method?.markup_ratio ?? method?.margin_ratio}
      />
    </div>
  );
}

function ReferenceFields({ method }: { method?: Method }) {
  const { t } = useTranslation();
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <TextField
        label={t("pricing.method.multiplier")}
        hint={t("pricing.method.multiplierHint")}
        name="multiplier"
        inputMode="decimal"
        autoComplete="off"
        defaultValue={method?.multiplier}
      />
      <SelectField
        label={t("pricing.method.conversion")}
        name="currency_conversion"
        defaultValue={method?.currency_conversion ?? "face"}
      >
        <option value="exchange">{t("pricing.method.exchange")}</option>
        <option value="face">{t("pricing.method.face")}</option>
      </SelectField>
    </div>
  );
}

// How the price is set: cost plus a markup or margin, a multiple of the model's reference price, or a price list of its
// own.
function MethodFields({ method }: { method?: Method }) {
  const { t } = useTranslation();
  const [kind, setKind] = useState<Method["kind"]>(method?.kind ?? "reference_multiplier");
  return (
    <>
      <SelectField
        label={t("pricing.rule.method")}
        name="method_kind"
        value={kind}
        onChange={(event) => setKind(methodKinds.find((k) => k === event.target.value) ?? "reference_multiplier")}
      >
        {methodKinds.map((value) => (
          <option key={value} value={value}>
            {t(`pricing.method.${value}`)}
          </option>
        ))}
      </SelectField>
      {kind === "cost_plus" ? <CostPlusFields method={method} /> : null}
      {kind === "reference_multiplier" ? <ReferenceFields method={method} /> : null}
      {kind === "fixed" ? <RatesFields schedule={method?.schedule} /> : null}
    </>
  );
}

function ConstraintFields({ constraints }: { constraints?: Rule["constraints"] }) {
  const { t } = useTranslation();
  return (
    <fieldset className="min-w-0 space-y-3">
      <legend className="mb-1 text-ui font-medium text-t2">{t("pricing.constraints.title")}</legend>
      <p className="text-label text-t4">{t("pricing.constraints.hint")}</p>
      <div className="grid gap-4 sm:grid-cols-3">
        <TextField
          label={t("pricing.constraints.minMargin")}
          name="min_margin_ratio"
          inputMode="decimal"
          autoComplete="off"
          defaultValue={constraints?.min_margin_ratio}
        />
        <TextField
          label={t("pricing.constraints.refMin")}
          name="reference_min_ratio"
          inputMode="decimal"
          autoComplete="off"
          defaultValue={constraints?.reference_min_ratio}
        />
        <TextField
          label={t("pricing.constraints.refMax")}
          name="reference_max_ratio"
          inputMode="decimal"
          autoComplete="off"
          defaultValue={constraints?.reference_max_ratio}
        />
      </div>
    </fieldset>
  );
}

// RuleFields is the whole of a rule: whether it applies, what it prices, for whom, how, and within what limits.
export function RuleFields({ rule }: { rule?: Rule }) {
  const { t } = useTranslation();
  return (
    <>
      <CheckField
        label={t("pricing.rule.enabled")}
        hint={t("pricing.rule.enabledHint")}
        name="enabled"
        defaultChecked={rule?.enabled ?? true}
      />
      <TargetFields target={rule?.target} />
      <AudienceFields audience={rule?.audience} />
      <MethodFields method={rule?.method} />
      <ConstraintFields constraints={rule?.constraints} />
    </>
  );
}
