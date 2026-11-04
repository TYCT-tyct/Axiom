import type { AdminSchemas } from "@axiom/api";
import { Td, useFormat } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Code } from "../parts";
import { cashMovementsQuery } from "../queries";
import { LedgerList } from "./ledger-list";
import { shortId } from "./states";
import { type LedgerListProps, useListWindow } from "./window";

// The cash behind the dashboard's figure. It is the ledger's own movements, by the transaction's time, so a refund of an
// order made before the window and an offline top-up both appear where the dashboard counted them.
export function CashMovements(props: LedgerListProps) {
  const { t } = useTranslation();
  const window = useListWindow(props.since, props.until);
  const cash = useInfiniteQuery(cashMovementsQuery(props.siteId, window));
  const rows = cash.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  return (
    <LedgerList
      title={t("orders.cash.title")}
      description={t("orders.cash.description")}
      back={t("orders.cash.back")}
      query={cash}
      rows={rows}
      emptyTitle={t("orders.cash.emptyTitle")}
      emptyBody={t("orders.cash.emptyBody")}
      columns={[
        [t("orders.cash.col.at")],
        [t("orders.cash.col.kind")],
        [t("orders.cash.col.account"), "hidden md:table-cell"],
        [t("orders.cash.col.amount")],
        [t("orders.cash.col.source"), "hidden sm:table-cell"],
      ]}
      keyOf={(movement, index) => movement.id ?? index}
      renderRow={(movement) => <CashRow movement={movement} />}
    />
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
