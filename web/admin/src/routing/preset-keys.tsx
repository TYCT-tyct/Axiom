import type { AdminSchemas } from "@axiom/api";
import { systemPreset } from "@axiom/app";
import { useFormat } from "@axiom/ui";
import { useTranslation } from "react-i18next";

// PresetKeys is how many keys of every account are bound to the preset. A node that does not count them, and the system
// preset, give no number: that is said, not shown as none.
export function PresetKeys({ preset }: { preset: AdminSchemas["RoutingPreset"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  if (preset.id === systemPreset) {
    return t("routingPresets.keys.system");
  }
  return preset.keys === undefined ? t("routingPresets.keys.unknown") : format.number(preset.keys);
}
