import { Icon, Panel } from "@axiom/ui";
import { useTranslation } from "react-i18next";
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
    <Panel title={t("suppliers.steps.title")}>
      <ol className="divide-y divide-b1">
        {all.map((step) => (
          <li key={step.key} className="flex items-start gap-3 px-4 py-3">
            <Icon name={step.done ? "check" : "alert"} className={step.done ? "mt-0.5 text-ok" : "mt-0.5 text-warn"} />
            <div className="min-w-0 flex-1">
              <p className="text-t1">{t(`suppliers.steps.${step.key}.title`)}</p>
              {step.done ? null : (
                <p className="text-t3">
                  {step.key === "keys" && step.failed
                    ? t("suppliers.steps.keys.failed", { count: step.failed })
                    : t(`suppliers.steps.${step.key}.todo`)}
                </p>
              )}
            </div>
            <span className="sr-only">{step.done ? t("suppliers.steps.done") : t("suppliers.steps.waiting")}</span>
            {!step.done && step.open && onOpen ? (
              <button
                type="button"
                className="shrink-0 text-accent underline-offset-4 hover:underline"
                onClick={() => step.open && onOpen(step.open)}
              >
                {t("suppliers.unit.open")}
              </button>
            ) : null}
          </li>
        ))}
      </ol>
    </Panel>
  );
}
