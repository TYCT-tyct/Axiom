import type { AdminSchemas } from "@axiom/api";
import { useFormat } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { State } from "../parts";

const tones = {
  connecting: "neutral",
  active: "good",
  low_balance: "warn",
  needs_attention: "bad",
  paused: "warn",
  removing: "warn",
  removed: "neutral",
} as const satisfies Record<AdminSchemas["SupplierStatus"], "neutral" | "good" | "warn" | "bad">;

// SupplierState names where a supplier stands, and for one that needs a person, why. The reason has no length limit in
// the contract, so a list shows its start (brief) and keeps rows short; the supplier's page shows all of it.
export function SupplierState({ supplier, brief = false }: { supplier: AdminSchemas["Supplier"]; brief?: boolean }) {
  const { t } = useTranslation();
  return (
    <div className="space-y-1">
      <State tone={tones[supplier.status]}>{t(`suppliers.state.${supplier.status}`)}</State>
      {supplier.status_reason ? (
        <p
          className={`max-w-64 text-label text-t3 [overflow-wrap:anywhere] ${brief ? "line-clamp-2" : ""}`}
          title={brief ? supplier.status_reason : undefined}
        >
          {supplier.status_reason}
        </p>
      ) : null}
    </div>
  );
}

// The balance as the supplier counts it, in its own unit: Axiom only turns it into money once the unit is valued.
export function NativeBalance({ supplier }: { supplier: AdminSchemas["Supplier"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  if (supplier.balance_native === undefined) {
    return <>{"—"}</>;
  }
  return (
    <>
      {t("suppliers.balanceNative", {
        value: format.decimal(supplier.balance_native),
        unit: t(`suppliers.native.${supplier.native_unit}`),
      })}
    </>
  );
}

// What one unit of the balance is worth, or the reminder that it is not yet known and the supplier stays out of routing.
export function UnitValue({ supplier }: { supplier: AdminSchemas["Supplier"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  if (supplier.unit_value === undefined) {
    return <State tone="warn">{t("suppliers.unit.unconfirmed")}</State>;
  }
  return (
    <>
      {t("suppliers.unit.worth", {
        unit: t(`suppliers.native.${supplier.native_unit}`),
        value: format.decimal(supplier.unit_value),
        currency: supplier.settlement_currency,
      })}
    </>
  );
}
