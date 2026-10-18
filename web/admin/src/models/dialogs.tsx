import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Icon, SelectField, TextField } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem, useSnapshot } from "../change";
import { Code } from "../parts";
import { readiness } from "./readiness";
import { readSpec, SpecFields } from "./spec-fields";

type Model = AdminSchemas["Model"];
const params = (model: Model, key: string) => ({ path: { model_id: model.id }, header: { "Idempotency-Key": key } });

// Replaces the baseline spec as of the revision the person read; the old one stays in the change history.
export function SpecDialog({ model: live, onClose }: { model: Model; onClose: () => void }) {
  const { t } = useTranslation();
  const [model, reload] = useSnapshot(live);
  return (
    <ChangeDialog
      reload={reload}
      title={t("models.spec.title")}
      intro={<p>{t("models.spec.intro")}</p>}
      action={t("models.spec.action")}
      fields={() => <SpecFields spec={model.spec} />}
      read={(values) => readSpec(values, t("models.spec.otherInvalid"))}
      send={(spec, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/models/{model_id}/spec", {
            params: params(model, key),
            body: { rev: model.rev, spec, reason },
          }),
        )
      }
      done={() => <p>{t("models.spec.done")}</p>}
      onClose={onClose}
    />
  );
}

type Named = Omit<AdminSchemas["NameAdd"], "reason">;

// A name is written as the vendor writes it. Names are never changed or removed, and no name can mean two models, so a
// name is added with care; only public names can be called by users.
export function NameDialog({ model, onClose }: { model: Model; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const read = (values: Record<string, string>): Named | Problem => {
    const name = (values.name ?? "").trim();
    return name
      ? { name, visibility: values.visibility === "internal" ? "internal" : "public" }
      : { problem: t("models.names.invalid") };
  };
  return (
    <ChangeDialog
      title={t("models.names.title")}
      intro={<p>{t("models.names.intro")}</p>}
      action={t("models.names.action")}
      fields={(error) => (
        <>
          <TextField
            label={t("models.names.name")}
            name="name"
            maxLength={200}
            autoComplete="off"
            spellCheck={false}
            defaultValue={model.slug}
            error={errors.field(error, "name")}
          />
          <SelectField label={t("models.names.visibility")} name="visibility" defaultValue="public">
            <option value="public">{t("models.names.public")}</option>
            <option value="internal">{t("models.names.internal")}</option>
          </SelectField>
        </>
      )}
      read={read}
      send={(name, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/models/{model_id}/names", {
            params: params(model, key),
            body: { ...name, reason },
          }),
        )
      }
      done={() => <p>{t("models.names.done")}</p>}
      onClose={onClose}
    />
  );
}

// Putting a draft on sale passes the review gate, which says what is missing when it fails; retiring takes a model out
// of use for good, and its names are never used again.
export function ModelStatusDialog({
  model: live,
  to,
  onClose,
}: {
  model: Model;
  to: "listed" | "retired";
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [model, reload] = useSnapshot(live);
  const checks = to === "listed" ? readiness(model) : [];
  return (
    <ChangeDialog
      reload={reload}
      title={t(`models.status.${to}.title`, { slug: model.slug })}
      intro={
        <>
          <p>{t(`models.status.${to}.body`)}</p>
          {checks.length > 0 ? (
            <ul className="space-y-1">
              {checks.map((check) => (
                <li key={check.key} className="flex items-start gap-2">
                  <Icon
                    name={check.ok ? "check" : "alert"}
                    className={check.ok ? "mt-0.5 text-ok" : "mt-0.5 text-warn"}
                  />
                  <span>{t(`models.ready.${check.key}`)}</span>
                </li>
              ))}
            </ul>
          ) : null}
        </>
      }
      action={t(`models.status.${to}.action`)}
      danger={to === "retired"}
      confirm={to === "retired" ? model.slug : undefined}
      read={() => to}
      send={(status, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/models/{model_id}/status", {
            params: params(model, key),
            body: { status, rev: model.rev, reason },
          }),
        )
      }
      done={(changed) => (
        <p>
          <Code>{changed.slug}</Code>
          {": "}
          {t(`models.state.${changed.status}`)}
        </p>
      )}
      onClose={onClose}
    />
  );
}
