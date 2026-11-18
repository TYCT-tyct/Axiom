import type { AdminSchemas } from "@axiom/api";
import { CheckField, Fieldset, Grid, TextArea } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import type { Problem } from "../change";
import { billingItems, serviceTiers } from "./catalog";

type Spec = AdminSchemas["ModelSpec"];

// The part of a spec that is not the two lists the review needs, as the person edits it: JSON, since the contract keeps
// the baseline facts (context length, modalities, ...) open for now.
export function otherFacts(spec: Spec | undefined) {
  if (!spec) {
    return "";
  }
  const { items: _items, tiers: _tiers, ...rest } = spec;
  return Object.keys(rest).length > 0 ? JSON.stringify(rest, null, 2) : "";
}

// SpecFields edits a model's baseline spec: which billing items and service tiers an upstream may report for it (a model
// goes on sale only if it declares both lists, even empty), and its other facts as JSON.
export function SpecFields({ spec }: { spec?: Spec }) {
  const { t } = useTranslation();
  return (
    <>
      <Fieldset legend={t("models.spec.items")} hint={t("models.spec.itemsHint")}>
        <Grid min="12rem" gap={8}>
          {billingItems.map((item) => (
            <CheckField
              key={item}
              label={t(`models.item.${item}`)}
              name={`item_${item}`}
              defaultChecked={spec?.items?.includes(item)}
            />
          ))}
        </Grid>
      </Fieldset>
      <Fieldset legend={t("models.spec.tiers")} hint={t("models.spec.tiersHint")}>
        <Grid min="12rem" gap={8}>
          {serviceTiers.map((tier) => (
            <CheckField
              key={tier}
              label={t(`models.tier.${tier}`)}
              name={`tier_${tier}`}
              defaultChecked={spec?.tiers?.includes(tier)}
            />
          ))}
        </Grid>
      </Fieldset>
      <TextArea
        label={t("models.spec.other")}
        hint={t("models.spec.otherHint")}
        name="other"
        rows={4}
        spellCheck={false}
        defaultValue={otherFacts(spec)}
      />
    </>
  );
}

// readSpec makes the spec from the typed fields: the declared lists, and the other facts if they are a JSON object.
export function readSpec(values: Record<string, string>, invalid: string): Spec | Problem {
  let other: unknown = {};
  const typed = (values.other ?? "").trim();
  if (typed) {
    try {
      other = JSON.parse(typed);
    } catch {
      return { problem: invalid };
    }
  }
  if (typeof other !== "object" || other === null || Array.isArray(other)) {
    return { problem: invalid };
  }
  return {
    ...other,
    items: billingItems.filter((item) => values[`item_${item}`] === "on"),
    tiers: serviceTiers.filter((tier) => values[`tier_${tier}`] === "on"),
  };
}
