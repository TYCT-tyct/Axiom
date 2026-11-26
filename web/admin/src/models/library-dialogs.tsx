import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Field, Select, Stack, Text } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem, useSnapshot } from "../change";
import { Code } from "../parts";

type Model = AdminSchemas["Model"];
type Source = AdminSchemas["ModelImportRequest"]["source"];

const familyPattern = /^[a-z0-9][a-z0-9.-]{0,63}$/;
const sources = ["litellm", "openrouter"] as const satisfies readonly Source[];

// Puts a model in a family, or takes it out, as of the revision the person read. A family is only a label of the
// catalog, so the change never touches a name.
export function FamilyDialog({ model: live, onClose }: { model: Model; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [model, reload] = useSnapshot(live);
  const read = (values: Record<string, string>): { family: string | null } | Problem => {
    const family = (values.family ?? "").trim();
    if (family === "") {
      return { family: null };
    }
    return familyPattern.test(family) ? { family } : { problem: t("models.family.invalid") };
  };
  return (
    <ChangeDialog
      operation={`model-family:${model.id}`}
      reload={reload}
      title={t("models.family.title", { slug: model.slug })}
      intro={<p>{t("models.family.intro")}</p>}
      action={t("models.family.action")}
      fields={(error) => (
        <Field
          label={t("models.family.field")}
          hint={t("models.family.hint")}
          name="family"
          maxLength={64}
          autoComplete="off"
          spellCheck={false}
          defaultValue={model.family}
          error={errors.field(error, "family")}
        />
      )}
      read={read}
      send={({ family }, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/models/{model_id}/family", {
            params: { path: { model_id: model.id }, header: { "Idempotency-Key": key } },
            body: { family, rev: model.rev, reason },
          }),
        )
      }
      done={(changed) => (
        <p>
          {t("models.family.done")} <Code>{changed.family ?? t("models.family.none")}</Code>
        </p>
      )}
      toast={() => t("models.family.done")}
      // The inverse is the family the model had, as of the revision this change made.
      undo={(changed, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/models/{model_id}/family", {
            params: { path: { model_id: model.id }, header: { "Idempotency-Key": key } },
            body: { family: model.family ?? null, rev: changed.rev, reason },
          }),
        )
      }
      onClose={onClose}
    />
  );
}

// Adds a draft model for each model a public price list has and the catalog lacks. Nothing goes on sale: a person
// reviews each draft first.
export function ImportDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation="model-import"
      title={t("models.import.title")}
      intro={<p>{t("models.import.intro")}</p>}
      action={t("models.import.action")}
      fields={() => (
        <Select label={t("models.import.source")} name="source" defaultValue="litellm">
          {sources.map((source) => (
            <option key={source} value={source}>
              {t(`models.import.sources.${source}`)}
            </option>
          ))}
        </Select>
      )}
      read={(values): { source: Source } => ({ source: values.source === "openrouter" ? "openrouter" : "litellm" })}
      send={({ source }, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/models/imports", {
            params: { header: { "Idempotency-Key": key } },
            body: { source, reason },
          }),
        )
      }
      done={(result) => (
        <Stack gap={4}>
          <p>{t("models.import.added", { count: result.added })}</p>
          <p>{t("models.import.known", { count: result.known })}</p>
          {result.held > 0 ? <p>{t("models.import.held", { count: result.held })}</p> : null}
          {result.held_names?.length ? (
            <Text size="sm" tone="faint">
              {t("models.import.heldNames")}
              {": "}
              {result.held_names.join(", ")}
            </Text>
          ) : null}
        </Stack>
      )}
      onClose={onClose}
    />
  );
}
