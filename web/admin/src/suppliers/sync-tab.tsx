import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { can, LoadMore, useSession } from "@axiom/app";
import { ActionButton, Card, RowList, Text, useFormat } from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "../change";
import type { SupplierView } from "../queries";
import { basisPoints } from "./inbox";
import { BuiltState } from "./parts";
import { reconciliationQuery, syncRunsQuery } from "./queries";
import { modeOf } from "./upstream";

type Run = AdminSchemas["SupplierSyncRun"];
type Day = AdminSchemas["SupplierReconciliationDay"];

// A day whose charges differ from the estimate by more than this is shown as one to look at: the queue's tolerance
// (docs/domains/suppliers.md「待处理队列」, cost_mismatch at 1%).
const tolerance = 100;

// 同步记录 (adPg_supplier's sync tab): when each part of the sync last worked, what each run that changed something
// found, and for a supplier whose charges are read, each day's estimate beside what it charged.
export default function SyncTab({ detail }: { detail: SupplierView }) {
  return (
    <>
      <Runs detail={detail} />
      <Reconciliation supplierId={detail.supplier.id} />
    </>
  );
}

function Runs({ detail }: { detail: SupplierView }) {
  const { t } = useTranslation();
  const format = useFormat();
  const runs = useInfiniteQuery(syncRunsQuery(detail.supplier.id));
  const rows = runs.data?.pages.flatMap((page) => page.data) ?? [];
  const { supplier } = detail;
  const what = t(`upstream.sync.lede.${modeOf(supplier)}`);
  const last = (detail.sync_tasks ?? [])
    .flatMap((task) => (task.last_ok_at ? [task.last_ok_at] : []))
    .sort()
    .at(-1);
  return (
    <Card
      layout="rows"
      title={t("upstream.sync.title")}
      lede={
        last
          ? t("upstream.sync.last", { what, at: format.dateTimeMinute(new Date(last)) })
          : t("upstream.sync.never", { what })
      }
      actions={<SyncNow supplier={supplier} />}
    >
      <BuiltState query={runs}>
        {() => (
          <>
            <RowList
              label={t("upstream.sync.title")}
              empty={t("upstream.sync.none")}
              alignTop
              items={rows.map((run) => ({
                key: run.version,
                title: t("upstream.sync.events", { count: run.events.length }),
                sub: describe(t, run),
                end: (
                  <Text as="span" size="sm" tone="faint" numeric>
                    {format.dateTimeMinute(new Date(run.at))}
                  </Text>
                ),
              }))}
            />
            <LoadMore list={runs} />
          </>
        )}
      </BuiltState>
    </Card>
  );
}

// A run's differences in words, Axiom's own names and values only (listSupplierSyncRuns never carries the supplier's).
function describe(t: ReturnType<typeof useTranslation>["t"], run: Run) {
  return run.events
    .map((event) =>
      t(`upstream.sync.event.${event.kind}` as "upstream.sync.event.ratio_changed", {
        subject: event.subject,
        old: event.old ?? "—",
        new: event.new ?? "—",
        why: event.why ?? "",
      }),
    )
    .join("；");
}

// 现在同步: every part of the sync made due now; what it changed shows in the list once it has run.
function SyncNow({ supplier }: { supplier: AdminSchemas["Supplier"] }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const synced = supplier.status === "connecting" || supplier.status === "active" || supplier.status === "low_balance";
  if (!can(useSession(), "suppliers.manage") || !synced || modeOf(supplier) !== "relay") return null;
  return (
    <>
      <ActionButton size="sm" icon="status" onClick={() => setOpen(true)}>
        {t("upstream.sync.now")}
      </ActionButton>
      {open ? (
        <ChangeDialog
          operation={`supplier-sync:${supplier.id}`}
          title={t("upstream.sync.nowTitle", { name: supplier.name })}
          intro={<p>{t("upstream.sync.nowBody")}</p>}
          action={t("upstream.sync.nowAction")}
          read={() => null}
          send={(_, { reason, key }) =>
            unwrapChange(
              adminApi.POST("/api/admin/v1/suppliers/{supplier_id}/sync-requests", {
                params: { path: { supplier_id: supplier.id }, header: { "Idempotency-Key": key } },
                body: { reason },
              }),
            )
          }
          done={() => null}
          toast={() => t("upstream.sync.queued")}
          onClose={() => setOpen(false)}
        />
      ) : null}
    </>
  );
}

function Reconciliation({ supplierId }: { supplierId: string }) {
  const { t } = useTranslation();
  const report = useQuery(reconciliationQuery(supplierId));
  if (!report.data || report.data.days.length === 0) return null;
  const { days, native_unit: unit } = report.data;
  return (
    <Card layout="rows" title={t("upstream.sync.reconcile")} lede={t("upstream.sync.reconcileLede")}>
      <RowList
        label={t("upstream.sync.reconcile")}
        items={days.map((day) => ({
          key: day.date,
          title: day.date,
          sub: <Amounts day={day} unit={unit} />,
          end: <Difference day={day} />,
        }))}
      />
    </Card>
  );
}

// The day's amounts as both sides count them: in the supplier's own unit, exactly.
function Amounts({ day, unit }: { day: Day; unit: AdminSchemas["SupplierReconciliation"]["native_unit"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  const money = (native: string) =>
    t("suppliers.balanceNative", { value: format.decimal(native), unit: t(`suppliers.native.${unit}`) });
  return (
    <>
      {t("upstream.sync.reconcileRow", { estimated: money(day.estimated_native), charged: money(day.charged_native) })}
    </>
  );
}

function Difference({ day }: { day: Day }) {
  const { t } = useTranslation();
  const bp = day.difference_bp;
  if (bp === undefined) {
    return (
      <Text as="span" size="sm" tone="faint">
        {t("upstream.sync.noEstimate")}
      </Text>
    );
  }
  const shown = basisPoints(bp);
  if (Math.abs(bp) <= tolerance) {
    return (
      <Text as="span" size="sm" tone="muted" numeric>
        {t("upstream.sync.diff", { percent: shown })}
      </Text>
    );
  }
  return (
    <Text as="span" size="sm" tone="warn" numeric>
      {t(bp > 0 ? "upstream.sync.above" : "upstream.sync.below", { percent: shown })}
    </Text>
  );
}
