import type { AdminSchemas } from "@axiom/api";
import { Text, useFormat } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { discrepanciesQuery, discrepancyStates } from "../finance-queries";
import { type Columns, type Format, ListTable } from "../list-table";
import { ShortId, State } from "../parts";
import { useSelected } from "../row-drawer";
import { DiscrepancyDrawer } from "./discrepancy-drawer";

type Discrepancy = AdminSchemas["PaymentDiscrepancy"];
type Standing = "all" | (typeof discrepancyStates)[number];

function discrepancyColumns(t: TFunction, format: Format): Columns<Discrepancy> {
  return [
    { key: "id", header: t("discrepancies.id"), cell: (row) => <ShortId id={row.id} /> },
    { key: "kind", from: "sm", header: t("discrepancies.kind"), cell: (row) => row.kind ?? "—" },
    {
      key: "order",
      from: "lg",
      header: t("discrepancies.order"),
      cell: (row) => <ShortId id={row.order_id} />,
    },
    {
      key: "account",
      from: "sm",
      header: t("discrepancies.account"),
      cell: (row) => <ShortId id={row.account_id} />,
    },
    {
      key: "created",
      from: "lg",
      header: t("discrepancies.created"),
      muted: true,
      cell: (row) => (row.created_at ? format.dateTime(new Date(row.created_at)) : "—"),
      sort: (row) => (row.created_at ? Date.parse(row.created_at) : null),
    },
    {
      key: "state",
      header: t("discrepancies.status"),
      cell: (row) =>
        row.state ? (
          <State tone={row.state === "open" ? "warn" : "good"}>{t(`discrepancies.state.${row.state}`)}</State>
        ) : (
          "—"
        ),
    },
  ];
}

// The payment differences tab: what the daily reconciliation found that the channel and the site's own records do not
// agree on, open ones first by when they were found. A row opens the two records side by side and the investigation. The
// state is the address's, as before (/orders/discrepancies?state=open).
export function DiscrepanciesTab() {
  const { t } = useTranslation();
  const format = useFormat();
  const navigate = useNavigate();
  const { state } = useSearch({ strict: false }) as { state?: (typeof discrepancyStates)[number] };
  const [selected, select] = useSelected();
  const [text, setText] = useState("");
  const query = useInfiniteQuery(discrepanciesQuery(state));
  const loaded = query.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  const word = text.trim().toLowerCase();
  const rows = loaded.filter((row) =>
    [row.id, row.kind, row.order_id, row.account_id].some((value) => value?.toLowerCase().includes(word)),
  );
  return (
    <>
      <Text tone="muted">{t("discrepancies.description")}</Text>
      <ListTable<Discrepancy, Standing>
        label={t("orders.tabs.discrepancies")}
        unit={t("discrepancies.unit")}
        search={{ label: t("discrepancies.search"), value: text, onChange: setText }}
        filter={{
          label: t("discrepancies.status"),
          value: state ?? "all",
          options: [
            { value: "all", label: t("discrepancies.allStates") },
            ...discrepancyStates.map((value) => ({ value, label: t(`discrepancies.state.${value}`) })),
          ],
          onChange: (value) =>
            void navigate({
              to: "/orders/discrepancies",
              search: { state: value === "all" ? undefined : value },
              replace: true,
            }),
        }}
        query={query}
        rows={rows}
        loaded={loaded.length}
        rowKey={(row) => row.id ?? ""}
        selected={selected}
        onOpen={(row) => row.id && select(row.id)}
        narrowed={Boolean(word) || Boolean(state)}
        empty={{ title: t("discrepancies.empty"), body: t("discrepancies.emptyBody") }}
        columns={discrepancyColumns(t, format)}
      />
      {selected ? (
        <DiscrepancyDrawer id={selected} keys={rows.flatMap((row) => (row.id ? [row.id] : []))} select={select} />
      ) : null}
    </>
  );
}
