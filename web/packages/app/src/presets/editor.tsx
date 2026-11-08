import { Button, CheckField, Icon, SelectField, TextField } from "@axiom/ui";
import type { UseQueryResult } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
  decimal,
  type Invalid,
  newOverride,
  type OverrideDraft,
  policies,
  priceItems,
  type RuleDraft,
  type RulesDraft,
  scored,
  sorts,
  withoutScore,
} from "./rules";

type Option = { value: string; label: string };
type Field = Invalid["field"] | undefined;

// The most overrides a preset holds (docs/domains/routing.md「规则内容」).
const maxOverrides = 100;

// The classes a rule can name, as the node routes by them: each app reads them from its own API.
type SourceClass = { slug: string; name: string; name_zh: string };
type ClassChoices = { options: Option[]; unavailable: boolean };

// useClassOptions offers the classes in the page's language. Unreadable, a rule keeps the choice it has and offers no
// other.
export function useClassOptions(classes: UseQueryResult<SourceClass[]>): ClassChoices {
  const { i18n } = useTranslation("common");
  const zh = i18n.language.startsWith("zh");
  const options = (classes.data ?? []).map((c) => ({ value: c.slug, label: (zh && c.name_zh) || c.name }));
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
// values checked. Limiting starts from every value offered, so the user removes what they don't want. The checkbox says
// what checking does; unchecked, the line under it says what holds instead.
function SetField({
  legend,
  limit,
  free,
  hint,
  error,
  options,
  value,
  onChange,
}: {
  legend: string;
  limit: string;
  free: string;
  hint?: string;
  error?: string;
  options: Option[];
  value: string[] | null;
  onChange: (value: string[] | null) => void;
}) {
  const { t } = useTranslation("common");
  const toggle = (option: string, on: boolean) =>
    onChange(on ? [...(value ?? []), option] : (value ?? []).filter((v) => v !== option));
  return (
    <fieldset className="space-y-2">
      <legend className="text-ui font-medium text-t2">{legend}</legend>
      <CheckField
        label={limit}
        hint={value === null ? t("presetRules.now", { state: free }) : undefined}
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
      {hint ? <p className="text-label text-t4">{hint}</p> : null}
      {error ? <Refusal>{error}</Refusal> : null}
    </fieldset>
  );
}

// Refusal says why a save was refused, under what it refuses; the dialog scrolls the first one into view.
function Refusal({ children }: { children: ReactNode }) {
  return (
    <p data-invalid className="text-label text-bad">
      {children}
    </p>
  );
}

type RuleProps = {
  rule: RuleDraft;
  parent: RuleDraft | null;
  invalid: Field;
  currency: string;
  classes: ClassChoices;
  onChange: (rule: RuleDraft) => void;
};

function SortField({ rule, parent, onChange }: Pick<RuleProps, "rule" | "parent" | "onChange">) {
  const { t } = useTranslation("common");
  return (
    <SelectField
      label={t("presetRules.sort")}
      value={parent ? rule.sort : rule.sort || "balanced"}
      onChange={(event) => onChange({ ...rule, sort: event.currentTarget.value })}
    >
      {parent ? <option value="">{t("presetRules.inherit")}</option> : null}
      {sorts.map((sort) => (
        <option key={sort} value={sort}>
          {t(`presetRules.sorts.${sort}`)}
        </option>
      ))}
    </SelectField>
  );
}

function SetFields({ rule, parent, invalid, classes, onChange }: Omit<RuleProps, "currency">) {
  const { t } = useTranslation("common");
  const free = parent ? t("presetRules.inherit") : null;
  const policyOptions = policies.map((p) => ({ value: p, label: t(`presetRules.policyNames.${p}`) }));
  return (
    <>
      <SetField
        legend={t("presetRules.classes")}
        limit={t("presetRules.limitClasses")}
        free={free ?? t("presetRules.allClasses")}
        hint={classes.unavailable ? t("presetRules.classesUnavailable") : undefined}
        error={
          invalid === "classes"
            ? t(rule.classes?.length === 0 ? "presetRules.invalid.classes" : "presetRules.unmatched.classes")
            : undefined
        }
        options={choices(classes.options, parent?.classes ?? null, rule.classes)}
        value={rule.classes}
        onChange={(value) => onChange({ ...rule, classes: value })}
      />
      <SetField
        legend={t("presetRules.policies")}
        limit={t("presetRules.limitPolicies")}
        free={free ?? t("presetRules.allPolicies")}
        hint={t("presetRules.policiesHint")}
        error={
          invalid === "policies"
            ? t(rule.policies?.length === 0 ? "presetRules.invalid.policies" : "presetRules.unmatched.policies")
            : undefined
        }
        options={choices(policyOptions, parent?.policies ?? null, rule.policies)}
        value={rule.policies}
        onChange={(value) => onChange({ ...rule, policies: value })}
      />
    </>
  );
}

function LimitFields({ rule, invalid, currency, onChange }: Omit<RuleProps, "parent" | "classes">) {
  const { t } = useTranslation("common");
  const wrong = (field: Field, message: string) => (invalid === field ? message : undefined);
  // The page refuses only a price it cannot write, so one it can is the server's: no endpoint sells at or below it.
  const price = t(
    priceItems.some((item) => invalid === item && decimal.test(rule.maxPrice[item].trim()))
      ? "presetRules.unmatched.price"
      : "presetRules.invalid.price",
  );
  return (
    <>
      <CheckField
        label={t("presetRules.verified")}
        checked={rule.verifiedOnly}
        onChange={(event) => onChange({ ...rule, verifiedOnly: event.currentTarget.checked })}
      />
      {invalid === "verified" ? <Refusal>{t("presetRules.unmatched.verified")}</Refusal> : null}
      {scored(rule) ? (
        <ScoreNotice refused={invalid === "score"} onRemove={() => onChange(withoutScore(rule))} />
      ) : null}
      <fieldset className="space-y-2">
        <legend className="text-ui font-medium text-t2">{t("presetRules.maxPrice", { currency })}</legend>
        <div className="grid gap-4 sm:grid-cols-3">
          {priceItems.map((item) => (
            <TextField
              key={item}
              label={t(`presetRules.prices.${item}`)}
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
        <p className="text-label text-t4">{t("presetRules.priceHint")}</p>
      </fieldset>
    </>
  );
}

// ScoreNotice stands for the score fields while scoring is not live: a rule read with a score (a copy, or one written
// through the API) says it matches nothing and offers to drop the score.
function ScoreNotice({ refused, onRemove }: { refused: boolean; onRemove: () => void }) {
  const { t } = useTranslation("common");
  const text = t("presetRules.scored");
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 rounded-btn bg-warn-soft px-3 py-2">
      {refused ? <Refusal>{text}</Refusal> : <p className="text-label text-t1">{text}</p>}
      <Button onClick={onRemove}>{t("presetRules.removeScore")}</Button>
    </div>
  );
}

function RuleFields(props: RuleProps) {
  const { t } = useTranslation("common");
  return (
    <div className="space-y-4">
      {props.invalid === "rule" ? <Refusal>{t("presetRules.unmatched.rule")}</Refusal> : null}
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
  classes,
  onChange,
  onRemove,
}: Omit<RuleProps, "rule" | "parent" | "onChange"> & {
  index: number;
  rule: OverrideDraft;
  base: RuleDraft;
  onChange: (rule: OverrideDraft) => void;
  onRemove: () => void;
}) {
  const { t } = useTranslation("common");
  return (
    <div className="space-y-4 rounded-btn border border-b1 p-4">
      <div className="flex items-center justify-between gap-2">
        <h4 className="text-ui font-medium text-t1">{t("presetRules.ruleTitle", { index: index + 1 })}</h4>
        <Button variant="ghost" size="icon" aria-label={t("presetRules.remove")} onClick={onRemove}>
          <Icon name="trash" />
        </Button>
      </div>
      <div className="grid gap-4 sm:grid-cols-[minmax(0,12rem)_minmax(0,1fr)]">
        <SelectField
          label={t("presetRules.target")}
          value={rule.target}
          onChange={(event) =>
            onChange({ ...rule, target: event.currentTarget.value === "model" ? "model" : "author" })
          }
        >
          <option value="author">{t("presetRules.author")}</option>
          <option value="model">{t("presetRules.model")}</option>
        </SelectField>
        <TextField
          label={t("presetRules.targetName")}
          hint={t("presetRules.targetHint")}
          value={rule.name}
          maxLength={200}
          autoComplete="off"
          spellCheck={false}
          error={invalid === "name" ? t("presetRules.invalid.name") : undefined}
          onChange={(event) => onChange({ ...rule, name: event.currentTarget.value })}
        />
      </div>
      <RuleFields
        rule={rule}
        parent={base}
        invalid={invalid}
        currency={currency}
        classes={classes}
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
  classes,
  onChange,
}: {
  draft: RulesDraft;
  invalid: Invalid | null;
  currency: string;
  classes: ClassChoices;
  onChange: (draft: RulesDraft) => void;
}) {
  const { t } = useTranslation("common");
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
      <Section title={t("presetRules.base")} hint={t("presetRules.baseHint")}>
        <RuleFields
          rule={draft.base}
          parent={null}
          invalid={fieldOf("default")}
          currency={currency}
          classes={classes}
          onChange={(base) => onChange({ ...draft, base })}
        />
      </Section>
      <Section title={t("presetRules.overrides")} hint={t("presetRules.overridesHint")}>
        {draft.overrides.map((rule, index) => (
          <OverrideCard
            key={rule.key}
            index={index}
            rule={rule}
            base={draft.base}
            invalid={fieldOf(index)}
            currency={currency}
            classes={classes}
            onChange={(next) => setOverride(index, next)}
            onRemove={() => setOverride(index, null)}
          />
        ))}
        {draft.overrides.length < maxOverrides ? (
          <Button size="md" onClick={() => onChange({ ...draft, overrides: [...draft.overrides, newOverride()] })}>
            <Icon name="plus" />
            {t("presetRules.add")}
          </Button>
        ) : null}
      </Section>
    </>
  );
}
