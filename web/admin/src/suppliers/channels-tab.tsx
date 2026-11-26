import type { AdminSchemas } from "@axiom/api";
import { LoadMore } from "@axiom/app";
import { Card, DataState, DataTable, EmptyState, MetricStrip, type StripItem, Text, useFormat } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { type Line, useLines } from "../lines";
import { suppliersQuery } from "../queries";
import { ModeFilter, type ModeFilters, ModeTag, NativeBalance, Rate, SupplierState, useTraffic } from "./parts";
import { modeOf, type Traffic } from "./upstream";

type Supplier = AdminSchemas["Supplier"];

// A balance that lasts fewer days than this is the one the page warns of (adSupStrip, adPg_suppliers).
const shortDays = 5;

// The channels (adPg_suppliers' list): every connected supplier, found by name, kind or group, and narrowed to a way
// in. Its success and share of traffic come from the last day's calls, for a session that may read the monitor.
export default function ChannelsTab() {
  const { t } = useTranslation();
  const suppliers = useInfiniteQuery(suppliersQuery());
  const lines = useLines();
  const traffic = useTraffic();
  const [filters, setFilters] = useState<ModeFilters>({ q: "", mode: "all" });
  const rows = suppliers.data?.pages.flatMap((page) => page.data) ?? [];
  const shown = rows.filter((supplier) => matches(supplier, lines.all, filters));
  return (
    <>
      {rows.length > 0 ? <Strip suppliers={rows} lines={lines.all} traffic={traffic} /> : null}
      <ModeFilter search={t("upstream.channels.search")} values={filters} onChange={setFilters} />
      <Card layout="flush">
        <DataState
          query={suppliers}
          isEmpty={() => shown.length === 0}
          empty={
            rows.length === 0 ? (
              <EmptyState title={t("upstream.channels.empty.title")}>{t("upstream.channels.empty.body")}</EmptyState>
            ) : (
              <EmptyState title={t("upstream.channels.noMatch.title")}>
                {t("upstream.channels.noMatch.body")}
              </EmptyState>
            )
          }
        >
          {() => <ChannelTable rows={shown} total={rows.length} lines={lines.all} traffic={traffic} />}
        </DataState>
        <LoadMore list={suppliers} />
      </Card>
    </>
  );
}

function matches(supplier: Supplier, lines: readonly Line[], { q, mode }: ModeFilters) {
  if (mode !== "all" && modeOf(supplier) !== mode) return false;
  const words = [
    supplier.name,
    supplier.kind,
    ...lines.filter((l) => l.supplier.id === supplier.id).map((l) => l.group.name),
  ];
  const typed = q.trim().toLowerCase();
  return typed === "" || words.some((word) => word.toLowerCase().includes(typed));
}

type Traffics = ReturnType<typeof useTraffic>;

function Strip({ suppliers, lines, traffic }: { suppliers: Supplier[]; lines: Line[]; traffic: Traffics }) {
  const { t } = useTranslation();
  const format = useFormat();
  const taking = suppliers.filter((supplier) => supplier.status === "active" || supplier.status === "low_balance");
  const low = suppliers.find((supplier) => supplier.balance_days !== undefined && supplier.balance_days < shortDays);
  const calls = traffic ? suppliers.map((supplier) => traffic.ofSupplier(supplier.id)) : [];
  const all = calls.reduce((sum, one) => sum + one.calls, 0);
  const rate = all > 0 ? calls.reduce((sum, one) => sum + (one.rate ?? 0) * one.calls, 0) / all : undefined;
  const items: StripItem[] = [
    {
      key: "count",
      label: t("upstream.channels.count"),
      value: format.number(suppliers.length),
      suffix: t("upstream.channels.unit.count"),
      note: t("upstream.channels.countNote", { count: taking.length }),
    },
    {
      key: "lines",
      label: t("upstream.channels.lines"),
      value: format.number(lines.length),
      suffix: t("upstream.channels.unit.lines"),
      note: t("upstream.channels.linesNote", { count: lines.filter((line) => line.usable).length }),
    },
    {
      key: "rate",
      label: t("upstream.channels.rate"),
      value: rate === undefined ? "—" : (rate * 100).toFixed(2),
      suffix: rate === undefined ? undefined : "%",
      note: t("upstream.channels.rateNote"),
    },
    {
      key: "balance",
      label: t("upstream.channels.balance"),
      value: low ? format.number(low.balance_days ?? 0) : "—",
      note: low ? (
        <Text as="span" tone="warn">
          {t("upstream.channels.balanceLow", { name: low.name, days: low.balance_days })}
        </Text>
      ) : (
        t("upstream.channels.balanceOk")
      ),
    },
  ];
  return (
    <MetricStrip label={t("upstream.channels.strip")} items={traffic ? items : items.filter((i) => i.key !== "rate")} />
  );
}

function ChannelTable({
  rows,
  total,
  lines,
  traffic,
}: {
  rows: Supplier[];
  total: number;
  lines: Line[];
  traffic: Traffics;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const of = (supplier: Supplier): Traffic | undefined => traffic?.ofSupplier(supplier.id);
  return (
    <DataTable
      label={t("upstream.tabs.channels")}
      rows={rows}
      total={total}
      rowKey={(row) => row.id}
      onOpen={(row) => void navigate({ to: "/suppliers/$supplierId", params: { supplierId: row.id } })}
      columns={[
        {
          key: "name",
          header: t("upstream.channels.col.name"),
          cell: (row) => <ChannelName supplier={row} lines={lines.filter((l) => l.supplier.id === row.id).length} />,
        },
        {
          key: "mode",
          header: t("upstream.channels.col.mode"),
          from: "md",
          cell: (row) => <ModeTag mode={modeOf(row)} />,
        },
        {
          key: "status",
          header: t("upstream.channels.col.status"),
          cell: (row) => <SupplierState supplier={row} brief />,
        },
        ...(traffic
          ? trafficColumns({ rate: t("upstream.channels.col.rate"), share: t("upstream.channels.col.share") }, of)
          : []),
        {
          key: "balance",
          header: t("upstream.channels.col.balance"),
          align: "end",
          cell: (row) => <NativeBalance supplier={row} />,
        },
        {
          key: "days",
          header: t("upstream.channels.col.days"),
          from: "md",
          sort: (row) => row.balance_days,
          cell: (row) => <Days days={row.balance_days} />,
        },
      ]}
    />
  );
}

// The last day's success and share of traffic, for a session that may read the monitor.
function trafficColumns(headers: { rate: string; share: string }, of: (supplier: Supplier) => Traffic | undefined) {
  return [
    {
      key: "rate",
      header: headers.rate,
      from: "lg" as const,
      sort: (row: Supplier) => of(row)?.rate,
      cell: (row: Supplier) => <Rate rate={of(row)?.rate} />,
    },
    {
      key: "share",
      header: headers.share,
      align: "end" as const,
      from: "md" as const,
      sort: (row: Supplier) => of(row)?.share,
      cell: (row: Supplier) => `${((of(row)?.share ?? 0) * 100).toFixed(0)}%`,
    },
  ];
}

function ChannelName({ supplier, lines }: { supplier: Supplier; lines: number }) {
  const { t } = useTranslation();
  const relay = modeOf(supplier) === "relay";
  return (
    <>
      <Text as="span" medium>
        {supplier.name}
      </Text>
      <Text size="sm" tone="muted">
        {t(relay ? "upstream.channels.groupsInUse" : "upstream.channels.linesCount", {
          kind: supplier.kind,
          count: lines,
        })}
      </Text>
    </>
  );
}

function Days({ days }: { days: number | undefined }) {
  const { t } = useTranslation();
  if (days === undefined) return <>{"—"}</>;
  const text = t("upstream.channels.days", { count: days });
  return days < shortDays ? (
    <Text as="span" tone="warn">
      {text}
    </Text>
  ) : (
    text
  );
}
