import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { CheckField, SelectField } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { ChangeDialog, isProblem, type Problem } from "../change";
import { handSources } from "./catalog";
import { PriceFields, readPrice, useInvalid } from "./price-fields";

type Model = AdminSchemas["Model"];
type Priced = { schedule: AdminSchemas["PriceSchedule"]; evidence: AdminSchemas["Evidence"] };

// The current row of a currency is what a new version builds on: if another version was published since the person
// looked, the server refuses (stale_revision) instead of putting this one over it.
function currentOf(model: Model, currency: string | undefined) {
  return model.prices.find((price) => price.currency === currency);
}

// Publishes a version of the model's reference price in one currency. Versions are never rewritten: a correction is
// another version.
export function PublishPriceDialog({
  model,
  currency,
  onClose,
}: {
  model: Model;
  currency?: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const invalid = useInvalid();
  const current = currentOf(model, currency);
  const read = (values: Record<string, string>): (Priced & { base?: string }) | Problem => {
    const priced = readPrice(values, current?.schedule, invalid);
    if (isProblem(priced)) {
      return priced;
    }
    const base = currentOf(model, priced.schedule.currency);
    return { ...priced, base: base?.id };
  };
  return (
    <ChangeDialog
      title={t("models.prices.publishTitle", { slug: model.slug })}
      intro={<p>{t("models.prices.publishIntro", { source: t(`models.source.${model.price_source}`) })}</p>}
      action={t("models.prices.publishAction")}
      fields={() => <PriceFields schedule={current?.schedule} currency={currency} />}
      read={read}
      send={({ base, schedule, evidence }, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/models/{model_id}/prices", {
            params: { path: { model_id: model.id }, header: { "Idempotency-Key": key } },
            body: {
              base: base as AdminSchemas["Uuid"] | undefined,
              source: model.price_source,
              confirm_basis_change: false,
              schedule,
              evidence,
              reason,
            },
          }),
        )
      }
      done={() => <p>{t("models.prices.published")}</p>}
      onClose={onClose}
    />
  );
}

// Switches the model to another source in one step: the new source's price in a currency is published and every other
// currency's price is withdrawn, so all the current prices come from the new source. M1 takes one currency here.
export function SourceDialog({ model, onClose }: { model: Model; onClose: () => void }) {
  const { t } = useTranslation();
  const invalid = useInvalid();
  const kept = model.prices.find((price) => price.schedule !== undefined);
  const read = (values: Record<string, string>) => {
    const priced = readPrice(values, undefined, invalid);
    if (isProblem(priced)) {
      return priced;
    }
    const source = handSources.find((candidate) => candidate === values.source) ?? "official";
    const bases = Object.fromEntries(model.prices.map((price) => [price.currency, price.id]));
    return { source, bases, prices: [priced], confirm: values.confirm_basis_change === "on" };
  };
  return (
    <ChangeDialog
      title={t("models.prices.sourceTitle", { slug: model.slug })}
      intro={
        <>
          <p>{t("models.prices.sourceIntro")}</p>
          <p>{t("models.prices.sourceWithdraws")}</p>
        </>
      }
      action={t("models.prices.sourceAction")}
      fields={() => (
        <>
          <SelectField label={t("models.prices.sourceChoose")} name="source" defaultValue="official">
            {handSources.map((source) => (
              <option key={source} value={source}>
                {t(`models.source.${source}`)}
              </option>
            ))}
          </SelectField>
          <PriceFields currency={kept?.currency} />
          <CheckField
            label={t("models.prices.confirmBasis")}
            hint={t("models.prices.confirmBasisHint")}
            name="confirm_basis_change"
          />
        </>
      )}
      read={read}
      send={({ source, bases, prices, confirm }, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/models/{model_id}/price-source", {
            params: { path: { model_id: model.id }, header: { "Idempotency-Key": key } },
            body: { rev: model.rev, source, bases, prices, confirm_basis_change: confirm, reason },
          }),
        )
      }
      done={(changed) => <p>{t("models.prices.sourceDone", { source: t(`models.source.${changed.price_source}`) })}</p>}
      onClose={onClose}
    />
  );
}
