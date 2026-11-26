import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { TextArea } from "@axiom/ui";
import { useQueries } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem, useSnapshot } from "../change";
import { Code } from "../parts";
import { modelQuery } from "../queries";

type Supplier = AdminSchemas["Supplier"];
type Rule = AdminSchemas["SupplierIntakeRule"];
type Mode = AdminSchemas["SupplierIntakeMode"];
type RuleInput = Pick<Rule, "mode" | "include" | "exclude">;

const lines = (text: string | undefined) => [...new Set((text ?? "").split(/\s+/).filter(Boolean))];

function putRule(supplier: Supplier, rule: RuleInput, rev: number, reason: string, key: string) {
  const names = rule.mode === "names";
  return unwrapChange(
    adminApi.PUT("/api/admin/v1/suppliers/{supplier_id}/intake-rule", {
      params: { path: { supplier_id: supplier.id }, header: { "Idempotency-Key": key } },
      body: {
        mode: rule.mode,
        include: names ? rule.include : undefined,
        exclude: names ? rule.exclude : undefined,
        rev,
        reason,
      },
    }),
  );
}

// 接入规则 (adPg_supplier's rule row): which of the supplier's names the sync takes, from the next catalog sync on. The
// change is made as of the rule the page read; the toast offers to put back the rule it replaced.
export function IntakeDialog({
  supplier,
  rule: live,
  to,
  onClose,
}: {
  supplier: Supplier;
  rule: Rule;
  to: Mode;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [rule, reload] = useSnapshot(live);
  const read = (values: Record<string, string>): RuleInput | Problem => {
    if (to !== "names") return { mode: to, include: [], exclude: [] };
    const include = lines(values.include);
    return include.length === 0
      ? { problem: t("upstream.intake.includeMissing") }
      : { mode: to, include, exclude: lines(values.exclude) };
  };
  return (
    <ChangeDialog
      operation={`intake-rule:${supplier.id}`}
      title={t("upstream.intake.title")}
      intro={<p>{supplier.name}</p>}
      diff={[
        {
          label: t("upstream.intake.row"),
          before: t(`upstream.models.rules.${rule.mode}`),
          after: t(`upstream.models.rules.${to}`),
        },
      ]}
      warn={t("upstream.intake.warn")}
      action={t("upstream.intake.action")}
      fields={(error) =>
        to === "names" ? (
          <>
            <TextArea
              label={t("upstream.intake.include")}
              hint={t("upstream.intake.includeHint")}
              name="include"
              rows={3}
              defaultValue={rule.include.join("\n")}
              spellCheck={false}
              error={errors.field(error, "include")}
            />
            <TextArea
              label={t("upstream.intake.exclude")}
              hint={t("upstream.intake.excludeHint")}
              name="exclude"
              rows={2}
              defaultValue={rule.exclude.join("\n")}
              spellCheck={false}
              error={errors.field(error, "exclude")}
            />
          </>
        ) : null
      }
      read={read}
      send={(input, { reason, key }) => putRule(supplier, input, rule.rev, reason, key)}
      reload={reload}
      done={() => null}
      toast={() => t("upstream.intake.done")}
      undo={(made, { reason, key }) => putRule(supplier, rule, made.rev, reason, key)}
      onClose={onClose}
    />
  );
}

// 上架这一族 (adA_famOn): the family's draft models the supplier lists, put on sale together, each as of the revision
// read when the dialog opened; their sale prices follow the pricing rules. The toast says how many were listed.
export function FamilyDialog({
  supplier,
  family,
  models,
  onClose,
}: {
  supplier: Supplier;
  family: string;
  models: readonly string[];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const read = useQueries({ queries: models.map((id) => modelQuery(id)) });
  const revs = read.flatMap((model) => (model.data ? [{ id: model.data.id, rev: model.data.rev }] : []));
  return (
    <ChangeDialog
      operation={`family-listing:${supplier.id}:${family}`}
      title={t("upstream.family.title")}
      intro={
        <p>
          <Code>{family}</Code>
        </p>
      }
      warn={t("upstream.family.warn")}
      action={t("upstream.family.action", { count: models.length })}
      read={() => (revs.length === models.length ? revs : { problem: t("upstream.family.reading") })}
      send={(listed, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/models/listings", {
            params: { header: { "Idempotency-Key": key } },
            body: { models: listed, reason },
          }),
        )
      }
      done={() => null}
      toast={(out) => {
        const listed = out.data.filter((one) => one.outcome === "listed").length;
        return listed === out.data.length
          ? t("upstream.family.done", { count: listed })
          : t("upstream.family.partly", { listed, rest: out.data.length - listed });
      }}
      onClose={onClose}
    />
  );
}
