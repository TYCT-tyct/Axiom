import { BrandSwatch, brandPreview, SettingRow, Stack, Text } from "@axiom/ui";
import { useTranslation } from "react-i18next";

// The default cyan accent from packages/ui/src/css/tokens.css when a site leaves its brand colour unset.
const defaultBrandColor = "#00a2c7";

// brandValid is whether typed text is a colour the site may keep: nothing (the default) or one brandStyle can derive its
// accents from.
export const brandValid = (text: string) => text.trim() === "" || brandPreview(text.trim()) !== undefined;

// BrandPreviewRow shows the light and dark accents brandStyle derives from the colour as typed, with their weakest
// WCAG contrast ratios, before it is submitted.
export function BrandPreviewRow({ color }: { color: string }) {
  const { t } = useTranslation();
  const typed = color.trim() === "" ? defaultBrandColor : color.trim();
  const preview = brandPreview(typed);
  if (!preview) {
    return null;
  }
  const adjusted =
    preview.light.accentText !== preview.input ||
    preview.dark.accentText !== preview.input ||
    preview.light.accentSolid !== preview.input ||
    preview.dark.accentSolid !== preview.input;
  return (
    <SettingRow title={t("settings.brand.preview")}>
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
    </SettingRow>
  );
}
