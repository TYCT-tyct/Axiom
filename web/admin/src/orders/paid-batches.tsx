import type { AdminSchemas } from "@axiom/api";
import { Td, useFormat } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Code } from "../parts";
import { paidBatchesQuery } from "../queries";
import { LedgerList } from "./ledger-list";
import { shortId } from "./states";
import { type LedgerListProps, useListWindow } from "./window";

// The paid credit batches behind the dashboard's paid users and monthly revenue. The list is by the batch's own
// created_at, not an order's creation time, so an order created before the window and credited inside it is here.
export function PaidBatches(props: LedgerListProps) {
  const { t } = useTranslation();
  const window = useListWindow(props.since, props.until);
  const batches = useInfiniteQuery(paidBatchesQuery(props.siteId, window));
  const rows = batches.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  return (
    <LedgerList
      title={t("orders.paidBatches.title")}
      description={t("orders.paidBatches.description")}
      back={t("orders.paidBatches.back")}
      query={batches}
      rows={rows}
      emptyTitle={t("orders.paidBatches.emptyTitle")}
      emptyBody={t("orders.paidBatches.emptyBody")}
      columns={[
        [t("orders.paidBatches.col.at")],
        [t("orders.paidBatches.col.account"), "hidden md:table-cell"],
        [t("orders.paidBatches.col.amount"), "text-right"],
        [t("orders.paidBatches.col.channel"), "hidden sm:table-cell"],
      ]}
      keyOf={(batch) => batch.id}
      renderRow={(batch) => <PaidBatchRow batch={batch} />}
    />
  );
}

function PaidBatchRow({ batch }: { batch: AdminSchemas["PaidBatch"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <tr>
      <Td>
        <Code title={batch.id}>{shortId(batch.id)}</Code>
        <span className="block text-label text-t3">{format.dateTime(new Date(batch.created_at))}</span>
      </Td>
      <Td className="hidden md:table-cell">
        <Code title={batch.account_id}>{shortId(batch.account_id)}</Code>
      </Td>
      <Td className="whitespace-nowrap text-right tabular-nums">{format.money(batch.amount)}</Td>
      <Td className="hidden sm:table-cell">
        {batch.online
          ? t("orders.paidBatches.online")
          : batch.offline_channel
            ? t("orders.paidBatches.offline", { channel: batch.offline_channel })
            : "—"}
      </Td>
    </tr>
  );
}
