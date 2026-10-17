import type { AdminSchemas } from "@axiom/api";
import { Icon, Panel } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import type { Open } from "./open";

type Detail = AdminSchemas["SupplierDetail"];

type Step = { key: "connected" | "groups" | "keys" | "unit"; done: boolean; open?: Open };

// The steps from a new supplier to one that takes traffic, read off what the sync stored: connected, a group chosen,
// a key bound for every chosen group, and the unit of its balance valued. A step is shown done or waiting, with the
// dialog that does it; the order is the order to work in.
function steps(detail: Detail): Step[] {
  const chosen = detail.groups.filter((group) => group.selected);
  const bound = new Set(detail.credentials.map((credential) => credential.group_id));
  const status = detail.supplier.status;
  return [
    { key: "connected", done: status !== "connecting" && status !== "needs_attention" },
    { key: "groups", done: chosen.length > 0 },
    { key: "keys", done: chosen.length > 0 && chosen.every((group) => bound.has(group.id)) },
    { key: "unit", done: detail.supplier.unit_value !== undefined, open: { kind: "unit" } },
  ];
}

export function Checklist({ detail, onOpen }: { detail: Detail; onOpen: ((open: Open) => void) | undefined }) {
  const { t } = useTranslation();
  const all = steps(detail);
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
              {step.done ? null : <p className="text-t3">{t(`suppliers.steps.${step.key}.todo`)}</p>}
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
