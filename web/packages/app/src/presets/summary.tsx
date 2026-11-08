import type { Schemas } from "@axiom/api";
import { useTranslation } from "react-i18next";
import { type Sort, sorts } from "./rules";

// RulesSummary is a preset's rules in a line, as a list shows them, from the summary the server makes of them: the
// default rule's sort, its sources, the verified threshold and price limits, and how many overrides follow. An older
// node, or rules this node cannot read, give none, and the line says so.
export function RulesSummary({ summary }: { summary: Schemas["RoutingRulesSummary"] | undefined }) {
  const { t } = useTranslation("common");
  if (!summary) {
    return <p className="text-t3">{t("presetRules.summary.unavailable")}</p>;
  }
  // A sort a newer node added has no words here yet; its name is shown as it is.
  const known = (sorts as readonly string[]).includes(summary.sort);
  const parts = [
    known ? t(`presetRules.sorts.${summary.sort as Sort}`) : summary.sort,
    summary.classes ? summary.classes.join(", ") : t("presetRules.summary.allClasses"),
    summary.verified_only ? t("presetRules.summary.verified") : null,
    summary.priced ? t("presetRules.summary.priced") : null,
  ].filter(Boolean);
  return (
    <>
      <p className="[overflow-wrap:anywhere] text-t2">{parts.join(" · ")}</p>
      {summary.overrides > 0 ? (
        <p className="text-label text-t4">{t("presetRules.summary.overrides", { count: summary.overrides })}</p>
      ) : null}
    </>
  );
}
