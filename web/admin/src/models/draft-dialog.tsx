import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Field, Select, TextLink } from "@axiom/ui";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { ChangeDialog, isProblem, type Problem } from "../change";
import { Code } from "../parts";
import { priceSources, versionBases } from "./catalog";
import { readSpec, SpecFields } from "./spec-fields";

type Draft = Omit<AdminSchemas["ModelCreate"], "reason">;
const slugPattern = /^[a-z0-9][a-z0-9-]{0,39}\/[a-z0-9][a-z0-9._-]{0,99}(@[a-z0-9][a-z0-9._-]{0,39})?$/;

// A new model starts as a draft that only the platform sees, until it passes the review gate and is put on sale. Its
// slug is `<author>/<model>`, with `@<version>` when the vendor gives one.
export function DraftDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const read = (values: Record<string, string>): Draft | Problem => {
    const slug = (values.slug ?? "").trim();
    const spec = readSpec(values, t("models.spec.otherInvalid"));
    if (!slugPattern.test(slug) || slug.length > 182) {
      return { problem: t("models.draft.slugInvalid") };
    }
    if (isProblem(spec)) {
      return spec;
    }
    return {
      slug,
      version_basis: versionBases.find((basis) => basis === values.version_basis) ?? "dated",
      price_source: priceSources.find((source) => source === values.price_source) ?? "official",
      spec,
    };
  };
  return (
    <ChangeDialog
      operation={"model-draft"}
      title={t("models.draft.title")}
      intro={<p>{t("models.draft.intro")}</p>}
      action={t("models.draft.action")}
      fields={(error) => <DraftFields error={error} />}
      read={read}
      send={(draft, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/models", {
            params: { header: { "Idempotency-Key": key } },
            body: { ...draft, reason },
          }),
        )
      }
      done={(model) => (
        <>
          <p>
            {t("models.draft.done")} <Code>{model.slug}</Code>
          </p>
          <TextLink>
            <Link to="/models/$modelId" params={{ modelId: model.id }}>
              {t("models.draft.review")}
            </Link>
          </TextLink>
        </>
      )}
      onClose={onClose}
    />
  );
}

function DraftFields({ error }: { error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <Field
        label={t("models.draft.slug")}
        hint={t("models.draft.slugHint")}
        name="slug"
        maxLength={182}
        autoComplete="off"
        spellCheck={false}
        error={errors.field(error, "slug")}
      />
      <Select label={t("models.draft.basis")} name="version_basis" defaultValue="dated">
        {versionBases.map((basis) => (
          <option key={basis} value={basis}>
            {t(`models.basis.${basis}`)}
          </option>
        ))}
      </Select>
      <Select label={t("models.draft.source")} name="price_source" defaultValue="official">
        {priceSources.map((source) => (
          <option key={source} value={source}>
            {t(`models.source.${source}`)}
          </option>
        ))}
      </Select>
      <SpecFields />
    </>
  );
}
