import type { AdminSchemas } from "@axiom/api";
import { useTranslation } from "react-i18next";
import { State } from "../parts";

const tones = { draft: "warn", listed: "good", retired: "neutral" } as const satisfies Record<
  AdminSchemas["ModelStatus"],
  "warn" | "good" | "neutral"
>;

// ModelState says whether a model is a draft the platform alone sees, on sale, or out of use for good.
export function ModelState({ status }: { status: AdminSchemas["ModelStatus"] }) {
  const { t } = useTranslation();
  return <State tone={tones[status]}>{t(`models.state.${status}`)}</State>;
}
