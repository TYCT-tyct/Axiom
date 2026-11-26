import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { ActionButton, Card, DataTable, EmptyState, PaceBar, StatusDot, Text, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import { rateTone } from "./health";
import { lineKey, useLineNames } from "./lines";
import { credentialsQuery } from "./monitor-queries";

type Line = AdminSchemas["MonitorLine"];
type Monitor = AdminSchemas["OperationsMonitor"];

// How full each line's credentials are now: the calls they hold against the calls they may hold, summed over the
// line's credentials, by the line's group.
function useConcurrency() {
  const query = useQuery({ ...credentialsQuery, enabled: can(useSession(), "suppliers.view") });
  const byGroup = new Map<string, { used: number; max: number }>();
  for (const credential of query.data?.data ?? []) {
    const own = byGroup.get(credential.group_id) ?? { used: 0, max: 0 };
    byGroup.set(credential.group_id, {
      used: own.used + (credential.in_flight ?? 0),
      max: own.max + credential.max_concurrency,
    });
  }
  return byGroup;
}

const okRate = (line: Line) => (line.calls > 0 ? (1 - line.failures / line.calls) * 100 : undefined);

// The columns every table of lines has: the line, how its calls went, and how fast the first token came.
function rateColumns(t: TFunction<"ops">) {
  return {
    rate: {
      key: "rate",
      header: t("lines.rate"),
      sort: okRate,
      cell: (line: Line) => {
        const value = okRate(line);
        return value === undefined ? (
          <Text as="span" tone="faint">
            {t("lines.none")}
          </Text>
        ) : (
          <StatusDot tone={rateTone(line)}>{`${value.toFixed(1)}%`}</StatusDot>
        );
      },
    },
    first: {
      key: "first",
      header: t("lines.first"),
      align: "end" as const,
      sort: (line: Line) => line.first_token_ms_p50,
      cell: (line: Line) =>
        line.first_token_ms_p50 === undefined
          ? t("lines.none")
          : t("lines.seconds", { value: (line.first_token_ms_p50 / 1000).toFixed(2) }),
    },
  };
}

// The columns of the two tables of lines: the dashboard's has each line's share of the traffic; the monitor's has the
// counts, and how full the line's credentials are.
function useColumns(monitor: Monitor, detailed: boolean) {
  const { t } = useTranslation("ops");
  const format = useFormat();
  const name = useLineNames();
  const busy = useConcurrency();
  const { rate, first } = rateColumns(t);
  const line = { key: "line", header: t("lines.line"), cell: name };
  const count = (key: "calls" | "failures") => ({
    key,
    header: t(`lines.${key}`),
    align: "end" as const,
    sort: (row: Line) => row[key],
    cell: (row: Line) => format.number(row[key]),
  });
  const bar = (label: string, value: number, max: number, width: number) => (
    <PaceBar label={label} value={value} max={max} width={width} />
  );
  const busyColumn = {
    key: "busy",
    header: t("lines.busy"),
    cell: (row: Line) => {
      const own = busy.get(row.group_id);
      return own ? bar(t("lines.busyBar", { name: name(row) }), own.used, own.max, 56) : t("lines.none");
    },
  };
  const shareColumn = {
    key: "share",
    header: t("lines.share"),
    cell: (row: Line) =>
      bar(t("lines.shareBar", { name: name(row) }), monitor.calls > 0 ? (row.calls / monitor.calls) * 100 : 0, 100, 72),
  };
  return detailed
    ? [
        line,
        count("calls"),
        { ...count("failures"), from: "sm" as const },
        rate,
        { ...first, from: "lg" as const },
        { ...busyColumn, from: "lg" as const },
      ]
    : [line, rate, first, shareColumn];
}

// LinesCard lists the platform's lines over the window the monitor was asked for (the board's 线路): how they went and how
// fast the first token came.
export function LinesCard({ monitor, detailed = false }: { monitor: Monitor; detailed?: boolean }) {
  const { t } = useTranslation("ops");
  const columns = useColumns(monitor, detailed);
  return (
    <Card
      layout="flush"
      title={t("lines.title")}
      lede={t(`lines.window.${monitor.window}`)}
      actions={
        <ActionButton asChild size="sm">
          {detailed ? (
            <Link to="/routing-presets">{t("lines.routing")}</Link>
          ) : (
            <Link to="/monitor" search={{ window: "1h" }}>
              {t("lines.monitor")}
            </Link>
          )}
        </ActionButton>
      }
    >
      {monitor.lines.length === 0 ? (
        <EmptyState compact title={t("lines.empty")} />
      ) : (
        <DataTable label={t("lines.title")} columns={columns} rows={monitor.lines} rowKey={lineKey} />
      )}
    </Card>
  );
}
