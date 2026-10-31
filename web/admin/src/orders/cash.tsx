import type { AdminSchemas } from "@axiom/api";
import { LoadMore } from "@axiom/app";
import { DataState, EmptyState, Panel, Table, Td, useFormat } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Code, Head } from "../parts";
import { cashMovementsQuery } from "../queries";
import { shortId } from "./states";

// The cash behind the dashboard's figure. It is the ledger's own movements, by the transaction's time, so a refund of an
// order made before the window and an offline top-up both appear where the dashboard counted them.
export function CashMovements({
  siteId,
  since,
  until,
}: {
  siteId: AdminSchemas["SiteId"];
  since?: string;
  until?: string;
}) {
  const { t } = useTranslation();
  const [anchor] = useState(() => new Date());
  const window = {
    since: since ?? new Date(anchor.getTime() - 24 * 60 * 60 * 1000).toISOString(),
    until: until ?? anchor.toISOString(),
  };
  const cash = useInfiniteQuery(cashMovementsQuery(siteId, window));
  const rows = cash.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  return (
    <Panel title={t("orders.cash.title")}>
      <p className="px-4 pt-4 text-t2">
        {t("orders.cash.description")}{" "}
        <Link to="/orders" className="text-accent underline-offset-4 hover:underline">
          {t("orders.cash.back")}
        </Link>
      </p>
      <DataState
        query={cash}
        isEmpty={() => rows.length === 0}
        empty={<EmptyState title={t("orders.cash.emptyTitle")}>{t("orders.cash.emptyBody")}</EmptyState>}
      >
        {() => (
          <Table label={t("orders.cash.title")}>
            <Head
              cols={[
                [t("orders.cash.col.at")],
                [t("orders.cash.col.kind")],
                [t("orders.cash.col.account"), "hidden md:table-cell"],
                [t("orders.cash.col.amount")],
                [t("orders.cash.col.source"), "hidden sm:table-cell"],
              ]}
            />
            <tbody>
              {rows.map((movement, index) => (
                <CashRow key={movement.id ?? index} movement={movement} />
              ))}
            </tbody>
          </Table>
        )}
      </DataState>
      <LoadMore list={cash} />
    </Panel>
  );
}

function CashRow({ movement }: { movement: AdminSchemas["CashMovement"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <tr>
      <Td>{movement.at ? format.dateTime(new Date(movement.at)) : "—"}</Td>
      <Td>{movement.kind ? t(`orders.cash.kind.${movement.kind}`) : "—"}</Td>
      <Td className="hidden md:table-cell">{movement.account_id ? <Code>{movement.account_id}</Code> : "—"}</Td>
      <Td>{movement.amount ? format.money(movement.amount) : "—"}</Td>
      <Td className="hidden sm:table-cell">
        {movement.offline_channel
          ? t("orders.cash.offline", { channel: movement.offline_channel })
          : movement.order_id
            ? t("orders.cash.online", { order: shortId(movement.order_id) })
            : "—"}
      </Td>
    </tr>
  );
}
