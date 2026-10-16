import type { AdminSchemas } from "@axiom/api";
import { useTranslation } from "react-i18next";
import { percent } from "../models/catalog";

// A rule method in one line, in the units a person thinks in: a markup or margin as a percentage.
export function MethodSummary({ method }: { method: AdminSchemas["PricingMethod"] }) {
  const { t } = useTranslation();
  if (method.kind === "cost_plus" && method.markup_ratio !== undefined) {
    return <>{t("pricing.describe.markup", { percent: percent(method.markup_ratio) })}</>;
  }
  if (method.kind === "cost_plus" && method.margin_ratio !== undefined) {
    return <>{t("pricing.describe.margin", { percent: percent(method.margin_ratio) })}</>;
  }
  if (method.kind === "reference_multiplier") {
    return <>{t("pricing.describe.reference", { multiplier: method.multiplier ?? "—" })}</>;
  }
  return <>{t("pricing.describe.fixed", { currency: method.schedule?.currency ?? "—" })}</>;
}
