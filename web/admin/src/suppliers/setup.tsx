import { can, useSession } from "@axiom/app";
import { ActionButton, Card, StepList } from "@axiom/ui";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useLines } from "../lines";
import type { SupplierView } from "../queries";
import { UnitValueDialog } from "./dialogs";
import { type Step, steps } from "./steps";
import { modeOf } from "./upstream";

type T = TFunction<"admin">;

// 接入检查 (adSupParts' setup): the steps from a new channel to one that takes traffic, in the words of its way in, shown
// until every one is done. The step to do now carries its action where the page has one.
export function Setup({ detail }: { detail: SupplierView }) {
  const { t } = useTranslation();
  const { byGroup } = useLines();
  const manage = can(useSession(), "suppliers.manage");
  const [unit, setUnit] = useState(false);
  const { supplier } = detail;
  const mode = modeOf(supplier);
  const all = steps({
    mode,
    status: supplier.status,
    groups: detail.groups,
    credentials: detail.credentials,
    endpoints: detail.endpoints,
    hasUnitValue: supplier.unit_value !== undefined,
    regionless: supplier.kind === "azure",
  });
  // The dialog that does the last step outlives the checklist it was opened from, which goes once every step is done:
  // it sits beside the card, in the same place either way, so it keeps what it showed.
  const dialog = unit ? <UnitValueDialog supplier={supplier} onClose={() => setUnit(false)} /> : null;
  const hidden = supplier.status === "removed" || all.every((step) => step.done);
  const lines = detail.groups
    .filter((group) => group.selected)
    .map((group) => byGroup.get(group.id)?.name ?? group.name)
    .join("、");
  const note = (step: Step) => noteOf(t, step, detail, lines);
  return (
    <>
      {hidden ? null : (
        <Card
          layout="rows"
          title={t("upstream.detail.setup")}
          lede={t("upstream.detail.setupNote", { count: all.length, done: all.filter((step) => step.done).length })}
        >
          <StepList
            label={t("upstream.detail.setup")}
            steps={all.map((step) => ({
              key: step.key,
              title: titleOf(t, step, mode),
              detail: note(step),
              done: step.done,
              action:
                step.key === "unit" && manage ? (
                  <ActionButton variant="pri" size="sm" onClick={() => setUnit(true)}>
                    {t("upstream.steps.unit.action")}
                  </ActionButton>
                ) : undefined,
            }))}
          />
        </Card>
      )}
      {dialog}
    </>
  );
}

function titleOf(t: T, step: Step, mode: ReturnType<typeof modeOf>) {
  if (step.key === "connected")
    return t(mode === "direct" ? "upstream.steps.connected.direct" : "upstream.steps.connected.relay");
  return t(`upstream.steps.${step.key}.title`);
}

// A step's line under its title, in one sentence of the words for it: the variant a step's state picks, and every value
// any of them names.
function noteOf(t: T, step: Step, detail: SupplierView, lines: string) {
  const { supplier } = detail;
  const live = detail.credentials.filter((credential) => credential.status === "active");
  const variant = step.failed ? "failed" : step.key === "groups" && lines ? "picked" : "note";
  return t(`upstream.steps.${step.key}.${variant}` as "upstream.steps.keys.failed", {
    origin: supplier.origin,
    account: supplier.upstream_account,
    groups: lines,
    lines,
    count: step.key === "models" ? (detail.endpoints?.length ?? 0) : live.length,
    failed: step.failed,
    regions: live.flatMap((credential) => (credential.region ? [credential.region] : [])).join("、"),
  });
}
