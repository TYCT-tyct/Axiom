import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { Card, RingChart, RowList, useFormat } from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { type Cause, type Deduction, type Health, healthOf } from "./health";
import { lineKey, useLineNames } from "./lines";
import { credentialsQuery } from "./monitor-queries";
import { alertEventsQuery } from "./policy-queries";
import { suppliersQuery } from "./queries";

type Monitor = AdminSchemas["OperationsMonitor"];

// The score of the shown window, from what the page shows (health.ts): the window's lines, the suppliers' balances, the
// alerts not yet resolved and the credentials at their ceiling. What the session may not read takes nothing off.
function useHealth(monitor: Monitor): { health: Health; supplier: (id: string) => string | undefined } {
  const session = useSession();
  const suppliers = useInfiniteQuery({ ...suppliersQuery(), enabled: can(session, "suppliers.view") });
  const alerts = useInfiniteQuery({ ...alertEventsQuery, enabled: can(session, "sites.manage") });
  const credentials = useQuery({ ...credentialsQuery, enabled: can(session, "suppliers.view") });
  const all = suppliers.data?.pages.flatMap((page) => page.data) ?? [];
  const health = healthOf({
    lines: monitor.lines.map((line) => ({
      supplier: line.supplier_id,
      group: line.group_id,
      calls: line.calls,
      failures: line.failures,
    })),
    suppliers: all.map((one) => ({ id: one.id, status: one.status, balanceDays: one.balance_days })),
    unresolvedAlerts: (alerts.data?.pages[0]?.data ?? []).filter((event) => event.state !== "resolved").length,
    credentials: credentials.data?.data ?? [],
  });
  return { health, supplier: (id) => all.find((one) => one.id === id)?.name };
}

// The rows of the points taken off: what each cost, why, and where its cause is.
function useRows(monitor: Monitor, health: Health, supplier: (id: string) => string | undefined) {
  const { t } = useTranslation("monitor");
  const name = useLineNames(monitor.lines);
  const lineOf = (cause: Extract<Cause, { kind: "line" }>) =>
    monitor.lines.find((line) => lineKey(line) === `${cause.supplier}:${cause.group}`);
  const row = ({ points, cause }: Deduction, at: number) => {
    const key = `${cause.kind}:${at}`;
    const end = `−${points}`;
    switch (cause.kind) {
      case "line": {
        const line = lineOf(cause);
        const rate = ((1 - cause.failures / cause.calls) * 100).toFixed(1);
        return {
          key,
          end,
          title: t("health.line", {
            name: line ? name(line) : cause.group.slice(0, 8),
            rate,
          }),
          link: <Link to="/suppliers/$supplierId" params={{ supplierId: cause.supplier }} />,
        };
      }
      case "balance":
        return {
          key,
          end,
          title: t("health.balance", { name: supplier(cause.supplier) ?? "", count: cause.days }),
          link: <Link to="/suppliers/$supplierId" params={{ supplierId: cause.supplier }} />,
        };
      case "alerts":
        return {
          key,
          end,
          title: t("health.alerts", { count: cause.count }),
          link: <Link to="/alert-events" />,
        };
      default:
        return {
          key,
          end,
          title: t("health.concurrency", { count: cause.count }),
          link: <Link to="/suppliers" search={{ status: undefined }} />,
        };
    }
  };
  return health.deductions.map(row);
}

// HealthCard is the score as a ring and, beside it, every point taken off and why (the board's 健康分). Each reason leads
// to the thing that caused it.
export function HealthCard({ monitor }: { monitor: Monitor }) {
  const { t } = useTranslation("monitor");
  const format = useFormat();
  const { health, supplier } = useHealth(monitor);
  const rows = useRows(monitor, health, supplier);
  const { verdict } = health;
  return (
    <Card title={t("health.title")} lede={t("health.lede")}>
      <RingChart
        label={t("health.ring", { score: health.score, verdict: t(`health.${verdict}`) })}
        size={148}
        parts={[
          { key: "score", label: "", value: health.score, share: "", colour: verdict === "watch" ? "warn" : verdict },
          { key: "rest", label: "", value: 100 - health.score, share: "", colour: "track" },
        ]}
        centre={format.number(health.score)}
        centreLabel={t(`health.${verdict}`)}
        aside={<RowList label={t("health.deductions")} empty={t("health.none")} items={rows} />}
      />
    </Card>
  );
}
