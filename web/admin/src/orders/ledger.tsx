import type { AdminSchemas } from "@axiom/api";
import { Text, useFormat } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { type ComponentProps, type Key, useState } from "react";
import { useTranslation } from "react-i18next";
import { type Columns, type Format, ListTable } from "../list-table";
import { Code } from "../parts";
import { cashMovementsQuery, paidBatchesQuery } from "../queries";
import { shortId } from "./states";
import { type LedgerListProps, useListWindow } from "./window";

type Table<T, V extends string> = ComponentProps<typeof ListTable<T, V>>;

// LedgerTable is a read-only ledger tab (the cash movements, the paid credit batches): a line saying what the ledger is, a
// search over what has been loaded, and a filter that keeps the rows of one way or kind. The page asks for the rows;
// what the search finds and what each filter keeps is the page's to say (matches, keep).
function LedgerTable<T, V extends string>({
  description,
  label,
  unit,
  searchLabel,
  filterLabel,
  options,
  keep,
  matches,
  loaded,
  query,
  columns,
  rowKey,
  empty,
}: {
  description: string;
  label: string;
  unit: string;
  searchLabel: string;
  filterLabel: string;
  options: readonly { value: V; label: string }[];
  keep: Record<V, (row: T) => boolean>;
  matches: (row: T, word: string) => boolean;
  loaded: readonly T[];
  query: Table<T, V>["query"];
  columns: Columns<T>;
  rowKey: (row: T) => Key;
  empty: { title: string; body: string };
}) {
  const [text, setText] = useState("");
  const [way, setWay] = useState<V>(options[0]?.value as V);
  const word = text.trim().toLowerCase();
  const rows = loaded.filter((row) => keep[way](row) && matches(row, word));
  return (
    <>
      <Text tone="muted">{description}</Text>
      <ListTable<T, V>
        label={label}
        unit={unit}
        search={{ label: searchLabel, value: text, onChange: setText }}
        filter={{ label: filterLabel, value: way, options, onChange: setWay }}
        query={query}
        rows={rows}
        loaded={loaded.length}
        rowKey={rowKey}
        narrowed={Boolean(word) || way !== options[0]?.value}
        empty={empty}
        columns={columns}
      />
    </>
  );
}

type Movement = AdminSchemas["CashMovement"];
type Kind = "all" | "money-in" | "money-out";

// What a kind of movement is, for the filter: a paid credit is money in; a refund, a chargeback and the reversal of a
// chargeback move it out or back, and the filter does not guess which way a reversal goes: it is with the refunds.
const kinds: Record<Kind, (movement: Movement) => boolean> = {
  all: () => true,
  "money-in": (movement) => movement.kind === "credit",
  "money-out": (movement) => movement.kind !== undefined && movement.kind !== "credit",
};

function cashColumns(t: TFunction, format: Format): Columns<Movement> {
  return [
    { key: "id", header: t("orders.cash.col.id"), cell: (movement) => <Code>{shortId(movement.id)}</Code> },
    {
      key: "account",
      header: t("orders.cash.col.account"),
      cell: (movement) => <Code title={movement.account_id}>{shortId(movement.account_id)}</Code>,
    },
    {
      key: "amount",
      header: t("orders.cash.col.amount"),
      align: "end",
      cell: (movement) => (movement.amount ? format.money(movement.amount) : "—"),
    },
    {
      key: "kind",
      header: t("orders.cash.col.kind"),
      cell: (movement) => (movement.kind ? t(`orders.cash.kind.${movement.kind}`) : "—"),
    },
    {
      key: "source",
      header: t("orders.cash.col.source"),
      cell: (movement) =>
        movement.offline_channel
          ? t("orders.cash.offline", { channel: movement.offline_channel })
          : movement.order_id
            ? t("orders.cash.online", { order: shortId(movement.order_id) })
            : "—",
    },
    {
      key: "at",
      header: t("orders.cash.col.at"),
      muted: true,
      cell: (movement) => (movement.at ? format.dateTime(new Date(movement.at)) : "—"),
      sort: (movement) => (movement.at ? Date.parse(movement.at) : null),
    },
  ];
}

// The cash tab: the ledger's cash movements in the window the address names (the dashboard's, or the last day), by the
// transaction's time, so a refund of an order made before the window and an offline top-up appear where the dashboard
// counted them. It only reads: nothing here changes a movement.
export function CashMovements({ siteId, since, until }: LedgerListProps) {
  const { t } = useTranslation();
  const format = useFormat();
  const query = useInfiniteQuery(cashMovementsQuery(siteId, useListWindow(since, until)));
  return (
    <LedgerTable<Movement, Kind>
      description={t("orders.cash.description")}
      label={t("orders.cash.title")}
      unit={t("orders.cash.unit")}
      searchLabel={t("orders.cash.search")}
      filterLabel={t("orders.cash.filter")}
      options={(["all", "money-in", "money-out"] as const).map((value) => ({
        value,
        label: t(`orders.cash.filters.${value}`),
      }))}
      keep={kinds}
      matches={(movement, word) =>
        [movement.id, movement.order_id, movement.account_id, movement.offline_channel].some((value) =>
          value?.toLowerCase().includes(word),
        )
      }
      loaded={query.data?.pages.flatMap((page) => page.data ?? []) ?? []}
      query={query}
      columns={cashColumns(t, format)}
      rowKey={(movement) => movement.id ?? ""}
      empty={{ title: t("orders.cash.emptyTitle"), body: t("orders.cash.emptyBody") }}
    />
  );
}

type Batch = AdminSchemas["PaidBatch"];
type Way = "all" | "online" | "offline";

const ways: Record<Way, (batch: Batch) => boolean> = {
  all: () => true,
  online: (batch) => batch.online,
  offline: (batch) => !batch.online,
};

function batchColumns(t: TFunction, format: Format): Columns<Batch> {
  return [
    {
      key: "id",
      header: t("orders.paidBatches.col.id"),
      cell: (batch) => <Code title={batch.id}>{shortId(batch.id)}</Code>,
    },
    {
      key: "account",
      header: t("orders.paidBatches.col.account"),
      cell: (batch) => <Code title={batch.account_id}>{shortId(batch.account_id)}</Code>,
    },
    {
      key: "amount",
      header: t("orders.paidBatches.col.amount"),
      align: "end",
      cell: (batch) => format.money(batch.amount),
    },
    {
      key: "from",
      header: t("orders.paidBatches.col.channel"),
      cell: (batch) =>
        batch.online
          ? t("orders.paidBatches.online")
          : batch.offline_channel
            ? t("orders.paidBatches.offline", { channel: batch.offline_channel })
            : "—",
    },
    {
      key: "at",
      header: t("orders.paidBatches.col.at"),
      muted: true,
      cell: (batch) => format.dateTime(new Date(batch.created_at)),
      sort: (batch) => Date.parse(batch.created_at),
    },
  ];
}

// The credit batches tab: the paid top-ups behind the dashboard's paid users and monthly revenue, by the batch's own time,
// so an order created before the window and credited inside it is here. Only paid batches are listed: a gift or a code's
// credit is not in this ledger.
export function PaidBatches({ siteId, since, until }: LedgerListProps) {
  const { t } = useTranslation();
  const format = useFormat();
  const query = useInfiniteQuery(paidBatchesQuery(siteId, useListWindow(since, until)));
  return (
    <LedgerTable<Batch, Way>
      description={t("orders.paidBatches.description")}
      label={t("orders.paidBatches.title")}
      unit={t("orders.paidBatches.unit")}
      searchLabel={t("orders.paidBatches.search")}
      filterLabel={t("orders.paidBatches.filter")}
      options={(["all", "online", "offline"] as const).map((value) => ({
        value,
        label: t(`orders.paidBatches.filters.${value}`),
      }))}
      keep={ways}
      matches={(batch, word) =>
        [batch.id, batch.account_id, batch.offline_channel].some((value) => value?.toLowerCase().includes(word))
      }
      loaded={query.data?.pages.flatMap((page) => page.data ?? []) ?? []}
      query={query}
      columns={batchColumns(t, format)}
      rowKey={(batch) => batch.id}
      empty={{ title: t("orders.paidBatches.emptyTitle"), body: t("orders.paidBatches.emptyBody") }}
    />
  );
}
