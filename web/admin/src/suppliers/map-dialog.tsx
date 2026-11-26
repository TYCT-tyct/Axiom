import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { CheckField, Select, Text } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "../change";
import { ModelField } from "../models/model-field";
import { Code } from "../parts";

// A name a supplier lists that no model matches, where it was seen, and the model whose name is closest, if any.
export type Unmatched = {
  supplierId: string;
  groupId: string;
  group?: string;
  upstreamName: string;
  closest?: string;
};

type Alias = Omit<AdminSchemas["ModelMappingRequest"], "reason">;

// An alias says what a name that a supplier lists means: this model, in one group or in the whole supplier (adA_mapDo).
// It is never changed afterwards, so the scope is chosen with care: a wrong alias is corrected by a narrower one.
export function MapDialog({ unmatched, onClose }: { unmatched: Unmatched; onClose: () => void }) {
  const { t } = useTranslation();
  const read = (values: Record<string, string>): Alias | Problem => {
    if (!values.model_id) {
      return { problem: t("upstream.map.modelMissing") };
    }
    return {
      upstream_name: unmatched.upstreamName,
      model_id: values.model_id as AdminSchemas["Uuid"],
      group_id: values.scope === "supplier" ? undefined : unmatched.groupId,
      // Only the format no adapter can report is offered here; the API takes the others. The projection unions the
      // declaration with what the adapter finds, so a name that already serves chat does not lose it.
      formats: values.completions ? ["completions"] : undefined,
    };
  };
  return (
    <ChangeDialog
      operation={`mapping:${unmatched.groupId}:${unmatched.upstreamName}`}
      title={t("upstream.map.title")}
      intro={
        <>
          <p>
            {t("upstream.map.body")} <Code>{unmatched.upstreamName}</Code>
          </p>
          {unmatched.closest ? <p>{t("upstream.map.guess", { slug: unmatched.closest })}</p> : null}
        </>
      }
      action={t("upstream.map.action")}
      fields={() => <MapFields group={unmatched.group} />}
      read={read}
      send={(alias, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/suppliers/{supplier_id}/model-mappings", {
            params: { path: { supplier_id: unmatched.supplierId }, header: { "Idempotency-Key": key } },
            body: { ...alias, reason },
          }),
        )
      }
      done={(mapping) => (
        <>
          <p>{t("upstream.map.done", { name: mapping.upstream_name })}</p>
          <Text size="sm" tone="faint">
            {t("upstream.map.wait")}
          </Text>
        </>
      )}
      onClose={onClose}
    />
  );
}

// What a mapping asks: the model, how far the alias reaches, and the one format no adapter reports.
function MapFields({ group }: { group?: string }) {
  const { t } = useTranslation();
  return (
    <>
      <ModelField label={t("upstream.map.model")} />
      <Select label={t("upstream.map.scope")} name="scope" defaultValue="group">
        <option value="group">
          {group ? `${t("upstream.map.scopeGroup")}（${group}）` : t("upstream.map.scopeGroup")}
        </option>
        <option value="supplier">{t("upstream.map.scopeSupplier")}</option>
      </Select>
      <CheckField name="completions" label={t("upstream.map.completions")} hint={t("upstream.map.completionsHint")} />
    </>
  );
}
