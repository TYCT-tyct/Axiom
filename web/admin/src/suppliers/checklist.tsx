import { ActionButton, Card, Cluster, SettingRow } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { State } from "../parts";
import type { SupplierView } from "../queries";
import type { Open } from "./open";
import { type Step, steps } from "./steps";

type Detail = SupplierView;

export function Checklist({ detail, onOpen }: { detail: Detail; onOpen: ((open: Open) => void) | undefined }) {
  const { t } = useTranslation();
  const all: Step[] = steps({
    status: detail.supplier.status,
    groups: detail.groups,
    credentials: detail.credentials,
    hasUnitValue: detail.supplier.unit_value !== undefined,
  });
  if (all.every((step) => step.done) || detail.supplier.status === "removed") {
    return null;
  }
  return (
    <Card layout="rows" title={t("suppliers.steps.title")}>
      {all.map((step) => (
        <SettingRow
          key={step.key}
          title={t(`suppliers.steps.${step.key}.title`)}
          detail={
            step.done
              ? undefined
              : step.key === "keys" && step.failed
                ? t("suppliers.steps.keys.failed", { count: step.failed })
                : t(`suppliers.steps.${step.key}.todo`)
          }
        >
          <Cluster>
            <State tone={step.done ? "good" : "warn"}>
              {step.done ? t("suppliers.steps.done") : t("suppliers.steps.waiting")}
            </State>
            {!step.done && step.open && onOpen ? (
              <ActionButton size="sm" onClick={() => step.open && onOpen(step.open)}>
                {t("suppliers.unit.open")}
              </ActionButton>
            ) : null}
          </Cluster>
        </SettingRow>
      ))}
    </Card>
  );
}
