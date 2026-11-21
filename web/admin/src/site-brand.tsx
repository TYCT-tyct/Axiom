import { type AdminSchemas, ApiError, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { ActionButton, BrandSwatch, brandPreview, DataState, Field, Stack, Text } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "./change";
import { Code, Facts } from "./parts";
import { siteBrandQuery } from "./queries";

// The default cyan accent from packages/ui/src/css/tokens.css when a site leaves its brand colour unset.
const defaultBrandColor = "#00a2c7";

function BrandPreviewBlock({ color }: { color: string | undefined }) {
  const { t } = useTranslation();
  const target = color && color.trim() !== "" ? color.trim() : defaultBrandColor;
  const preview = brandPreview(target);
  if (!preview) {
    return null;
  }
  const adjusted =
    preview.light.accentText !== preview.input ||
    preview.dark.accentText !== preview.input ||
    preview.light.accentSolid !== preview.input ||
    preview.dark.accentSolid !== preview.input;
  return (
    <Stack gap={8}>
      <BrandSwatch
        color={preview.input}
        lightLabel={t("settings.brand.light")}
        darkLabel={t("settings.brand.dark")}
        sample={t("settings.brand.sample")}
        contrastLabel={(text, solid) => t("settings.brand.contrast", { text, solid })}
      />
      <Text size="sm" tone="muted">
        {t(adjusted ? "settings.brand.adjusted" : "settings.brand.unchanged")}
      </Text>
    </Stack>
  );
}

// SiteBrand displays and edits the site's #rrggbb brand colour, previewing the light and dark accents brandStyle
// derives from it along with their weakest WCAG contrast ratios.
export function SiteBrand({ siteId }: { siteId: AdminSchemas["SiteId"] }) {
  const { t } = useTranslation();
  const brand = useQuery(siteBrandQuery(siteId));
  const [open, setOpen] = useState(false);
  if (brand.error instanceof ApiError && brand.error.status === 404) {
    return (
      <Facts
        title={t("settings.brand.title")}
        rows={[[t("settings.brand.unavailableLabel"), t("settings.brand.unavailable")]]}
      />
    );
  }
  return (
    <DataState query={brand}>
      {(view) => (
        <>
          <Facts
            title={t("settings.brand.title")}
            actions={
              <ActionButton onClick={() => setOpen(true)} icon="pencil">
                {t("settings.brand.edit")}
              </ActionButton>
            }
            rows={[
              [
                t("settings.brand.color"),
                view.brand_color ? (
                  <Code>{view.brand_color}</Code>
                ) : (
                  <Text as="span" tone="muted">
                    {t("settings.brand.default")}
                  </Text>
                ),
              ],
              [t("settings.brand.preview"), <BrandPreviewBlock key="preview" color={view.brand_color} />],
            ]}
          />
          {open ? (
            <EditBrandDialog siteId={siteId} current={view.brand_color ?? ""} onClose={() => setOpen(false)} />
          ) : null}
        </>
      )}
    </DataState>
  );
}

function BrandFields({ initial, error }: { initial: string; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [typed, setTyped] = useState(initial);
  const trimmed = typed.trim();
  const valid = trimmed === "" || brandPreview(trimmed) !== undefined;
  return (
    <Stack gap={12}>
      <Field
        label={t("settings.brand.color")}
        hint={t("settings.brand.hint")}
        name="brand_color"
        placeholder={defaultBrandColor}
        maxLength={7}
        autoComplete="off"
        value={typed}
        onChange={(event) => setTyped(event.currentTarget.value)}
        error={errors.field(error, "brand_color")}
      />
      {valid ? (
        <BrandPreviewBlock color={trimmed || undefined} />
      ) : (
        <Text size="sm" tone="muted">
          {t("settings.brand.invalid")}
        </Text>
      )}
    </Stack>
  );
}

function EditBrandDialog({
  siteId,
  current,
  onClose,
}: {
  siteId: AdminSchemas["SiteId"];
  current: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`site-brand:${siteId}`}
      title={t("settings.brand.editTitle")}
      intro={<p>{t("settings.brand.intro")}</p>}
      action={t("settings.brand.save")}
      fields={(error) => <BrandFields initial={current} error={error} />}
      read={(values): string | Problem => {
        const value = (values.brand_color ?? "").trim();
        if (value === "") {
          return "";
        }
        return brandPreview(value) ? value.toLowerCase() : { problem: t("settings.brand.invalid") };
      }}
      send={(brand_color, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/brand", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { brand_color, reason },
          }),
        )
      }
      done={(saved) => (
        <p>
          {saved.brand_color ? t("settings.brand.saved", { color: saved.brand_color }) : t("settings.brand.cleared")}
        </p>
      )}
      onClose={onClose}
    />
  );
}
