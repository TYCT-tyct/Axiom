import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { readForm } from "@axiom/app";
import { Button } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, isProblem, type Problem } from "../change";
import { useInvalid } from "../models/price-fields";
import { listedModelsQuery } from "../queries";
import { type PreviewRequest, PreviewResult } from "./preview";
import { readRule } from "./read-rule";
import { type Rule, RuleFields } from "./rule-fields";

type PricingRule = AdminSchemas["PricingRule"];

function useWords() {
  const { t } = useTranslation();
  return {
    ...useInvalid(),
    target: t("pricing.rule.invalid.target"),
    audience: t("pricing.rule.invalid.audience"),
    ratio: t("pricing.rule.invalid.ratio"),
    multiplier: t("pricing.rule.invalid.multiplier"),
    constraint: t("pricing.rule.invalid.constraint"),
  };
}

// How many models a rule's own preview prices: enough to see what the rule does, few enough to read in a dialog.
const previewModels = 20;

// RulePreview prices a few models with the rule as typed so far, in place of the rule being changed, before it is
// saved. The button reads the form it sits in.
function RulePreview({ replaces, existing }: { replaces?: PricingRule; existing?: Rule }) {
  const { t } = useTranslation();
  const words = useWords();
  const models = useQuery(listedModelsQuery);
  const [request, setRequest] = useState<PreviewRequest | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  return (
    <section className="space-y-3 rounded-btn border border-b2 p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-ui font-medium text-t2">{t("pricing.preview.title")}</h3>
        <Button
          size="sm"
          disabled={!models.data}
          onClick={(event) => {
            const form = event.currentTarget.form;
            const rule = form ? readRule(readForm(form), words, existing) : null;
            if (rule === null || isProblem(rule)) {
              setProblem(rule?.problem ?? null);
              return;
            }
            setProblem(null);
            setRequest({
              model_ids: (models.data?.data ?? []).slice(0, previewModels).map((model) => model.id),
              tier_id: rule.audience.kind === "tier" ? (rule.audience.id as AdminSchemas["TierId"]) : undefined,
              account_id:
                rule.audience.kind === "account" ? (rule.audience.id as AdminSchemas["AccountId"]) : undefined,
              rule,
              replaces_rule_id: replaces?.id,
            });
          }}
        >
          {t("pricing.rule.previewRun")}
        </Button>
      </div>
      {problem ? (
        <p role="alert" className="text-bad">
          {problem}
        </p>
      ) : null}
      <PreviewResult request={request} />
    </section>
  );
}

// A rule says what it prices, for whom, how, and within what limits. Two rules with the same target and audience cannot
// both exist, so a rule is changed rather than added twice; it is never deleted, only turned off, so the history stays.
export function RuleDialog({ rule, onClose }: { rule?: PricingRule; onClose: () => void }) {
  const { t } = useTranslation();
  const words = useWords();
  const read = (values: Record<string, string>): Rule | Problem => readRule(values, words, rule);
  return (
    <ChangeDialog
      title={rule ? t("pricing.rule.editTitle") : t("pricing.rule.newTitle")}
      intro={<p>{t("pricing.rule.intro")}</p>}
      action={rule ? t("pricing.rule.save") : t("pricing.rule.create")}
      fields={() => (
        <>
          <RuleFields rule={rule} />
          <RulePreview replaces={rule} existing={rule} />
        </>
      )}
      read={read}
      send={(input, { reason, key }) =>
        rule
          ? unwrapChange(
              adminApi.PUT("/api/admin/v1/pricing/rules/{rule_id}", {
                params: { path: { rule_id: rule.id }, header: { "Idempotency-Key": key } },
                body: { rev: rule.rev, rule: input, reason },
              }),
            )
          : unwrapChange(
              adminApi.POST("/api/admin/v1/pricing/rules", {
                params: { header: { "Idempotency-Key": key } },
                body: { rule: input, reason },
              }),
            )
      }
      done={(saved) => <p>{t("pricing.rule.saved", { state: saved.enabled ? t("pricing.on") : t("pricing.off") })}</p>}
      onClose={onClose}
    />
  );
}
