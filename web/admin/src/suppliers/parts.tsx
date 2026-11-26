import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { DataState, FilterBar, type PanelSteps, Stack, StatusDot, Tag, Text, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import type { ComponentProps } from "react";
import { useTranslation } from "react-i18next";
import { monitorQuery, notBuilt } from "../monitor-queries";
import { State } from "../parts";
import { type Mode, modes, trafficOf } from "./upstream";

// How a supplier is reached, as a quiet tag: 中转站, 直连接口, 云平台.
export function ModeTag({ mode }: { mode: Mode }) {
  const { t } = useTranslation();
  return <Tag>{t(`upstream.mode.${mode}`)}</Tag>;
}

// BuiltState is DataState for a section whose operation a node of an earlier release does not have: there it is left
// out, saying nothing, and it appears once the node answers.
export function BuiltState<T>(props: ComponentProps<typeof DataState<T>>) {
  return notBuilt(props.query.error) ? null : <DataState {...props} />;
}

// What the last day's calls say of each supplier and line, for a session that may read the operations monitor; none
// for one that may not, or before the monitor answers.
export function useTraffic() {
  const allowed = can(useSession(), "sites.manage");
  const monitor = useQuery({ ...monitorQuery("24h"), enabled: allowed });
  return monitor.data ? trafficOf(monitor.data.lines ?? []) : undefined;
}

const tones = {
  connecting: "neutral",
  active: "good",
  low_balance: "warn",
  needs_attention: "bad",
  paused: "warn",
  removing: "warn",
  removed: "neutral",
} as const satisfies Record<AdminSchemas["SupplierStatus"], "neutral" | "good" | "warn" | "bad">;

// SupplierState names where a supplier stands, and for one that needs a person, why. The reason is at most 200
// characters in the contract, which a list still shows as its start (brief) to keep rows short; the supplier's page shows
// all of it.
export function SupplierState({ supplier, brief = false }: { supplier: AdminSchemas["Supplier"]; brief?: boolean }) {
  const { t } = useTranslation();
  return (
    <Stack gap={4}>
      <div>
        <State tone={tones[supplier.status]}>{t(`suppliers.state.${supplier.status}`)}</State>
      </div>
      {supplier.status_reason ? (
        <Text size="sm" tone="muted" lines={brief ? 2 : undefined} title={brief ? supplier.status_reason : undefined}>
          {supplier.status_reason}
        </Text>
      ) : null}
    </Stack>
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

// A share of calls that did not fail, as the board writes it beside its dot; under 99% is one to watch.
export function Rate({ rate }: { rate: number | undefined }) {
  if (rate === undefined) return <>{"—"}</>;
  return <StatusDot tone={rate < 0.99 ? "warn" : "ok"}>{`${(rate * 100).toFixed(2)}%`}</StatusDot>;
}

export type ModeFilters = { q: string; mode: "all" | Mode };

// The row above a list of channels or keys: a search box, the ways in to narrow it to (中转站, 直连接口, 云平台), and how
// many of the list's rows (total) they leave.
export function ModeFilter({
  search,
  values,
  onChange,
  count,
}: {
  search: string;
  values: ModeFilters;
  onChange: (values: ModeFilters) => void;
  count: { shown: number; total: number };
}) {
  const { t } = useTranslation();
  return (
    <FilterBar
      label={search}
      filters={[
        { key: "q", kind: "search", label: search, initial: "" },
        {
          key: "mode",
          kind: "segmented",
          label: t("upstream.channels.filter"),
          initial: "all",
          options: [
            { value: "all", label: t("upstream.all") },
            ...modes.map((mode) => ({ value: mode, label: t(`upstream.mode.${mode}`) })),
          ],
        },
      ]}
      values={values}
      onChange={(key, value) => onChange({ ...values, [key]: value })}
      onClear={() => onChange({ q: "", mode: "all" })}
      count={{ ...count, unit: t("upstream.rows") }}
    />
  );
}

// The steps of a drawer over a list (PanelSteps): the row before or after the open one, by its ID.
export function stepsOf<Row extends { id: string }>(
  t: (key: "drawerSteps.previous" | "drawerSteps.next") => string,
  index: number,
  rows: readonly Row[],
  go: (id: string | undefined) => void,
): PanelSteps {
  return {
    index,
    count: rows.length,
    onStep: (by) => go(rows[index + by]?.id),
    previousLabel: t("drawerSteps.previous"),
    nextLabel: t("drawerSteps.next"),
  };
}
