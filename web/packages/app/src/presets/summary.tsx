import type { Schemas } from "@axiom/api";
import { useTranslation } from "react-i18next";
import { type Sort, sorts } from "./rules";

type Text = (key: never, options?: never) => string;

// summaryParts is a preset's rules as a list shows them, from the summary the server makes of them: the default rule's
// sort, its sources, the verified threshold and price limits. A sort a newer node added has no words here yet, so its
// name is shown as it is; an older node, or rules this node cannot read, give none.
function summaryParts(t: Text, summary: Schemas["RoutingRulesSummary"] | undefined): string[] {
  const text = (key: string) => t(key as never) as string;
  if (!summary) {
    return [text("presetRules.summary.unavailable")];
  }
  return [
    (sorts as readonly string[]).includes(summary.sort)
      ? text(`presetRules.sorts.${summary.sort as Sort}`)
      : summary.sort,
    summary.classes ? summary.classes.join(", ") : text("presetRules.summary.allClasses"),
    summary.verified_only ? text("presetRules.summary.verified") : null,
    summary.priced ? text("presetRules.summary.priced") : null,
  ].filter((part): part is string => part !== null);
}

// summaryLine is those parts and how many overrides follow, in one line, for a table's cell.
export function summaryLine(t: Text, summary: Schemas["RoutingRulesSummary"] | undefined) {
  const overrides = (key: string, count: number) => (t as (k: string, o: unknown) => string)(key, { count });
  return [
    ...summaryParts(t, summary),
    summary && summary.overrides > 0 ? overrides("presetRules.summary.overrides", summary.overrides) : null,
  ]
    .filter((part) => part !== null)
    .join(" · ");
}

// RulesSummary is that line where a page has room for it: the overrides on a line of their own.
export function RulesSummary({ summary }: { summary: Schemas["RoutingRulesSummary"] | undefined }) {
  const { t } = useTranslation("common");
  return (
    <>
      <p className="[overflow-wrap:anywhere] text-t2">{summaryParts(t, summary).join(" · ")}</p>
      {summary && summary.overrides > 0 ? (
        <p className="text-label text-t4">{t("presetRules.summary.overrides", { count: summary.overrides })}</p>
      ) : null}
    </>
  );
}
