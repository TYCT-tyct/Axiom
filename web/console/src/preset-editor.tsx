import { Button, CheckField, Icon, SelectField, TextField } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
  type Invalid,
  newOverride,
  type OverrideDraft,
  policies,
  priceItems,
  type RuleDraft,
  type RulesDraft,
  sorts,
} from "./preset-rules";
import { sourceClassesQuery } from "./queries";

type Option = { value: string; label: string };
type Field = Invalid["field"] | undefined;

// The most overrides a preset holds (docs/domains/routing.md「规则内容」).
const maxOverrides = 100;

// The classes a rule can name, in the page's language, as the node routes by them. Unreadable, a rule keeps the choice
// it has and offers no other.
function useClassOptions(): { options: Option[]; unavailable: boolean } {
  const { i18n } = useTranslation();
  const classes = useQuery(sourceClassesQuery);
  const zh = i18n.language.startsWith("zh");
  const options = (classes.data?.data ?? []).map((c) => ({ value: c.slug, label: (zh && c.name_zh) || c.name }));
  return { options, unavailable: classes.isError };
}

// The options a list offers: what the rule above allows (all of them when it is not limited), and whatever the rule
// already names beyond those, so an edit never drops a value the page cannot show.
function choices(all: Option[], parent: string[] | null, value: string[] | null) {
  const allowed = parent === null ? all : all.filter((option) => parent.includes(option.value));
  const extra = (value ?? []).filter((v) => !allowed.some((option) => option.value === v));
  return [...allowed, ...extra.map((v) => ({ value: v, label: v }))];
}

// SetField is an allow list: unlimited (no limit on the default rule, the rule above's on an override), or exactly the
// values checked. Limiting starts from every value offered, so the user removes what they don't want.
function SetField({
  legend,
  limit,
  free,
  hint,
  options,
  value,
  onChange,
}: {
  legend: string;
  limit: string;
  free: string;
  hint?: string;
  options: Option[];
  value: string[] | null;
  onChange: (value: string[] | null) => void;
}) {
  const toggle = (option: string, on: boolean) =>
    onChange(on ? [...(value ?? []), option] : (value ?? []).filter((v) => v !== option));
  return (
    <fieldset className="space-y-2">
      <legend className="text-ui font-medium text-t2">{legend}</legend>
      <CheckField
        label={value === null ? `${limit} · ${free}` : limit}
        hint={hint}
        checked={value !== null}
        // With nothing to offer, limiting would write an empty list, which allows no endpoint at all.
        disabled={value === null && options.length === 0}
        onChange={(event) => onChange(event.currentTarget.checked ? options.map((o) => o.value) : null)}
      />
      {value !== null ? (
        <div className="flex flex-wrap gap-x-5 gap-y-2 ps-6">
          {options.map((option) => (
            <CheckField
              key={option.value}
              label={option.label}
              checked={value.includes(option.value)}
              onChange={(event) => toggle(option.value, event.currentTarget.checked)}
            />
          ))}
        </div>
      ) : null}
    </fieldset>
  );
}

type RuleProps = {
  rule: RuleDraft;
  parent: RuleDraft | null;
  invalid: Field;
  currency: string;
  onChange: (rule: RuleDraft) => void;
};

function SortField({ rule, parent, onChange }: Pick<RuleProps, "rule" | "parent" | "onChange">) {
  const { t } = useTranslation();
  return (
    <SelectField
      label={t("routingPresets.editor.sort")}
      value={parent ? rule.sort : rule.sort || "balanced"}
      onChange={(event) => onChange({ ...rule, sort: event.currentTarget.value })}
    >
      {parent ? <option value="">{t("routingPresets.editor.inherit")}</option> : null}
      {sorts.map((sort) => (
        <option key={sort} value={sort}>
          {t(`routingPresets.editor.sorts.${sort}`)}
        </option>
      ))}
    </SelectField>
  );
}

function SetFields({ rule, parent, onChange }: Pick<RuleProps, "rule" | "parent" | "onChange">) {
  const { t } = useTranslation();
  const classes = useClassOptions();
  const free = parent ? t("routingPresets.editor.inherit") : null;
  const policyOptions = policies.map((p) => ({ value: p, label: t(`routingPresets.editor.policyNames.${p}`) }));
  return (
    <>
      <SetField
        legend={t("routingPresets.editor.classes")}
        limit={t("routingPresets.editor.limitClasses")}
        free={free ?? t("routingPresets.editor.allClasses")}
        hint={classes.unavailable ? t("routingPresets.editor.classesUnavailable") : undefined}
        options={choices(classes.options, parent?.classes ?? null, rule.classes)}
        value={rule.classes}
        onChange={(value) => onChange({ ...rule, classes: value })}
      />
      <SetField
        legend={t("routingPresets.editor.policies")}
        limit={t("routingPresets.editor.limitPolicies")}
        free={free ?? t("routingPresets.editor.allPolicies")}
        hint={t("routingPresets.editor.policiesHint")}
        options={choices(policyOptions, parent?.policies ?? null, rule.policies)}
        value={rule.policies}
        onChange={(value) => onChange({ ...rule, policies: value })}
      />
    </>
  );
}

function LimitFields({ rule, invalid, currency, onChange }: Omit<RuleProps, "parent">) {
  const { t } = useTranslation();
  const wrong = (field: Field, message: string) => (invalid === field ? message : undefined);
  const price = t("routingPresets.editor.invalid.price");
  return (
    <>
      <CheckField
        label={t("routingPresets.editor.verified")}
        checked={rule.verifiedOnly}
        onChange={(event) => onChange({ ...rule, verifiedOnly: event.currentTarget.checked })}
      />
      <div className="grid gap-4 sm:grid-cols-2">
        <TextField
          label={t("routingPresets.editor.minScore")}
          inputMode="decimal"
          value={rule.minScore}
          error={wrong("minScore", t("routingPresets.editor.invalid.minScore"))}
          onChange={(event) => onChange({ ...rule, minScore: event.currentTarget.value })}
        />
        <TextField
          label={t("routingPresets.editor.minConfidence")}
          inputMode="decimal"
          value={rule.minConfidence}
          error={wrong("minConfidence", t("routingPresets.editor.invalid.minConfidence"))}
          onChange={(event) => onChange({ ...rule, minConfidence: event.currentTarget.value })}
        />
      </div>
      <p className="text-label text-t4">{t("routingPresets.editor.scoreHint")}</p>
      <fieldset className="space-y-2">
        <legend className="text-ui font-medium text-t2">{t("routingPresets.editor.maxPrice", { currency })}</legend>
        <div className="grid gap-4 sm:grid-cols-3">
          {priceItems.map((item) => (
            <TextField
              key={item}
              label={t(`routingPresets.editor.prices.${item}`)}
              inputMode="decimal"
              autoComplete="off"
              value={rule.maxPrice[item]}
              error={wrong(item, price)}
              onChange={(event) =>
                onChange({ ...rule, maxPrice: { ...rule.maxPrice, [item]: event.currentTarget.value } })
              }
            />
          ))}
        </div>
        <p className="text-label text-t4">{t("routingPresets.editor.priceHint")}</p>
      </fieldset>
    </>
  );
}

function RuleFields(props: RuleProps) {
  return (
    <div className="space-y-4">
      <SortField {...props} />
      <SetFields {...props} />
      <LimitFields {...props} />
    </div>
  );
}

function Section({ title, hint, children }: { title: string; hint: string; children: ReactNode }) {
  return (
    <section className="space-y-4 border-t border-b1 pt-4">
      <div className="space-y-1">
        <h3 className="text-ui font-medium text-t1">{title}</h3>
        <p className="text-label text-t3">{hint}</p>
      </div>
      {children}
    </section>
  );
}

function OverrideCard({
  index,
  rule,
  base,
  invalid,
  currency,
  onChange,
  onRemove,
}: Omit<RuleProps, "rule" | "parent" | "onChange"> & {
  index: number;
  rule: OverrideDraft;
  base: RuleDraft;
  onChange: (rule: OverrideDraft) => void;
  onRemove: () => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="space-y-4 rounded-btn border border-b1 p-4">
      <div className="flex items-center justify-between gap-2">
        <h4 className="text-ui font-medium text-t1">{t("routingPresets.editor.ruleTitle", { index: index + 1 })}</h4>
        <Button variant="ghost" size="icon" aria-label={t("routingPresets.editor.remove")} onClick={onRemove}>
          <Icon name="trash" />
        </Button>
      </div>
      <div className="grid gap-4 sm:grid-cols-[minmax(0,12rem)_minmax(0,1fr)]">
        <SelectField
          label={t("routingPresets.editor.target")}
          value={rule.target}
          onChange={(event) =>
            onChange({ ...rule, target: event.currentTarget.value === "model" ? "model" : "author" })
          }
        >
          <option value="author">{t("routingPresets.editor.author")}</option>
          <option value="model">{t("routingPresets.editor.model")}</option>
        </SelectField>
        <TextField
          label={t("routingPresets.editor.targetName")}
          hint={t("routingPresets.editor.targetHint")}
          value={rule.name}
          maxLength={200}
          autoComplete="off"
          spellCheck={false}
          error={invalid === "name" ? t("routingPresets.editor.invalid.name") : undefined}
          onChange={(event) => onChange({ ...rule, name: event.currentTarget.value })}
        />
      </div>
      <RuleFields
        rule={rule}
        parent={base}
        invalid={invalid}
        currency={currency}
        onChange={(next) => onChange({ ...rule, ...next })}
      />
    </div>
  );
}

// RulesEditor edits a preset's rules: the rule every model is held to, then the overrides by vendor or model. An
// override's lists offer only what the default rule allows, since it may only narrow it.
export function RulesEditor({
  draft,
  invalid,
  currency,
  onChange,
}: {
  draft: RulesDraft;
  invalid: Invalid | null;
  currency: string;
  onChange: (draft: RulesDraft) => void;
}) {
  const { t } = useTranslation();
  const fieldOf = (rule: Invalid["rule"]) => (invalid?.rule === rule ? invalid.field : undefined);
  const setOverride = (index: number, rule: OverrideDraft | null) =>
    onChange({
      ...draft,
      overrides: rule
        ? draft.overrides.map((o, i) => (i === index ? rule : o))
        : draft.overrides.filter((_, i) => i !== index),
    });
  return (
    <>
      <Section title={t("routingPresets.editor.base")} hint={t("routingPresets.editor.baseHint")}>
        <RuleFields
          rule={draft.base}
          parent={null}
          invalid={fieldOf("default")}
          currency={currency}
          onChange={(base) => onChange({ ...draft, base })}
        />
      </Section>
      <Section title={t("routingPresets.editor.overrides")} hint={t("routingPresets.editor.overridesHint")}>
        {draft.overrides.map((rule, index) => (
          <OverrideCard
            key={rule.key}
            index={index}
            rule={rule}
            base={draft.base}
            invalid={fieldOf(index)}
            currency={currency}
            onChange={(next) => setOverride(index, next)}
            onRemove={() => setOverride(index, null)}
          />
        ))}
        {draft.overrides.length < maxOverrides ? (
          <Button size="md" onClick={() => onChange({ ...draft, overrides: [...draft.overrides, newOverride()] })}>
            <Icon name="plus" />
            {t("routingPresets.editor.add")}
          </Button>
        ) : null}
      </Section>
    </>
  );
}
