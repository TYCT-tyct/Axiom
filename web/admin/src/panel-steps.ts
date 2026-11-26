import type { PanelSteps } from "@axiom/ui";
import { useTranslation } from "react-i18next";

// The "2 / 6" and the previous and next of a drawer opened from a list: it walks the rows the list holds, in the order
// it shows them. A row no longer in the list (a filter dropped it) has no place in it, so the drawer has no steps.
export function usePanelSteps<Row>(
  rows: readonly Row[],
  selected: string,
  keyOf: (row: Row) => string | undefined,
  select: (key: string) => void,
): PanelSteps | undefined {
  const { t } = useTranslation();
  const index = rows.findIndex((row) => keyOf(row) === selected);
  if (index < 0) return undefined;
  return {
    index,
    count: rows.length,
    onStep: (by) => {
      const next = rows[index + by];
      const key = next === undefined ? undefined : keyOf(next);
      if (key !== undefined) select(key);
    },
    previousLabel: t("drawerSteps.previous"),
    nextLabel: t("drawerSteps.next"),
  };
}
