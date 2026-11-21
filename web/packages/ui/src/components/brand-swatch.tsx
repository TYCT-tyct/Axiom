import type { CSSProperties } from "react";
import { type BrandSchemePreview, brandPreview } from "../brand";
import s from "./brand-swatch.module.css";

type SchemeCardProps = {
  label: string;
  sample: string;
  contrastLabel: (text: string, solid: string) => string;
  scheme: BrandSchemePreview;
};

function SchemeCard({ label, sample, contrastLabel, scheme }: SchemeCardProps) {
  return (
    <div
      className={s.card}
      style={
        {
          "--sw-bg": scheme.surface,
          "--sw-ink": scheme.ink,
          "--sw-accent": scheme.accentText,
          "--sw-solid": scheme.accentSolid,
        } as CSSProperties
      }
    >
      <div className={s.head}>
        <span className={s.chip} aria-hidden="true" />
        <span className={s.title}>{label}</span>
        <span className={s.hex}>{scheme.accentSolid}</span>
      </div>
      <div className={s.accent}>{sample}</div>
      <div className={s.meta}>{contrastLabel(scheme.textContrast.toFixed(1), scheme.solidContrast.toFixed(1))}</div>
    </div>
  );
}

// BrandSwatch previews the light and dark accents brandStyle derives from a #rrggbb colour, with each scheme's own
// page surface and weakest contrast ratios.
export function BrandSwatch({
  color,
  lightLabel,
  darkLabel,
  sample,
  contrastLabel,
}: {
  color: string;
  lightLabel: string;
  darkLabel: string;
  sample: string;
  contrastLabel: (text: string, solid: string) => string;
}) {
  const preview = brandPreview(color);
  if (!preview) {
    return null;
  }
  return (
    <div className={s.grid}>
      <SchemeCard label={lightLabel} sample={sample} contrastLabel={contrastLabel} scheme={preview.light} />
      <SchemeCard label={darkLabel} sample={sample} contrastLabel={contrastLabel} scheme={preview.dark} />
    </div>
  );
}
