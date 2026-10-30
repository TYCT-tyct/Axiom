import type { Page } from "@playwright/test";
import { brandStyle } from "../packages/ui/src/brand.ts";

// Brand colours at the edges (docs/frontend.md「验收门槛」), applied the way a site's colour setting will be.
export const brands = {
  red: "#ff0000",
  yellow: "#ffff00",
  black: "#050505",
  white: "#fafafa",
  green: "#39ff14",
} as const;

export async function applyBrand(page: Page, color: string) {
  const style = brandStyle(color) ?? {};
  await page.evaluate((tokens) => {
    for (const [name, value] of Object.entries(tokens)) {
      document.documentElement.style.setProperty(name, value);
    }
  }, style);
}
