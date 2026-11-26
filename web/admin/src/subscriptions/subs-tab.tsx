import type { AdminSchemas } from "@axiom/api";
import { Cluster, EmptyState, moneyInput, PaceBar, Text, useFormat } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { type Columns, type Format, ListTable } from "../list-table";
import { shortId } from "../orders/states";
import { Code, State } from "../parts";
import { useAddressParam, useSelected } from "../row-drawer";
import { isNotImplemented, platformSubscriptionsQuery } from "../subscription-queries";
import { Ends, usedShare } from "./ends";
import { SubscriptionDrawer } from "./subscription-drawer";
import { SummaryStrip } from "./summary-strip";

type Subscription = AdminSchemas["Subscription"];
type Standing = "all" | AdminSchemas["SubscriptionStanding"];
const standings = ["all", "active", "expired"] as const satisfies readonly Standing[];

const accountId = /^acct_[0-9a-hjkmnp-tv-z]{26}$/;
function subscriptionColumns(t: TFunction, format: Format, now: number): Columns<Subscription> {
  return [
    {
      key: "account",
      header: t("subscriptions.col.account"),
      cell: (sub) => <Code title={sub.account_id}>{shortId(sub.account_id)}</Code>,
    },
    { key: "plan", header: t("subscriptions.col.plan"), cell: (sub) => sub.plan_name },
    {
      key: "quota",
      header: t("subscriptions.col.quota"),
      sort: usedShare,
      cell: (sub) => (
        <Cluster>
          <PaceBar label={t("subscriptions.col.quota")} value={usedShare(sub)} max={1} width={72} />
          <Text as="span" tone="muted" size="sm" numeric>
            {`${moneyInput(sub.current_window.used)} / ${moneyInput(sub.current_window.quota)}`}
          </Text>
        </Cluster>
      ),
    },
    {
      key: "ends",
      header: t("subscriptions.col.ends"),
      sort: (sub) => Date.parse(sub.ends_at),
      cell: (sub) => <Ends sub={sub} now={now} />,
    },
    {
      key: "state",
      header: t("subscriptions.col.state"),
      cell: (sub) => (
        <State tone={sub.state === "active" ? "good" : "neutral"}>
          {t(`subscriptions.account.state.${sub.state}`)}
        </State>
      ),
    },
    {
      key: "started",
      header: t("subscriptions.col.started"),
      muted: true,
      cell: (sub) => format.dayFull(new Date(sub.starts_at)),
    },
  ];
}

// The list of subscriptions as the address and the filter ask for it: the address's search (q) goes to the server when it is
// a whole account ID, and otherwise finds a word in what has been loaded; the state goes to the server.
function useSubscriptionList() {
  const { address: q, text, setText } = useAddressParam("q");
  const [standing, setStanding] = useState<Standing>("all");
  const exact = q && accountId.test(q) ? (q as AdminSchemas["AccountId"]) : undefined;
  const query = useInfiniteQuery(
    platformSubscriptionsQuery({ state: standing === "all" ? undefined : standing, account_id: exact }),
  );
  const loaded = query.data?.pages.flatMap((page) => page.data) ?? [];
  const word = exact ? "" : (q ?? "").toLowerCase();
  const rows = loaded.filter((sub) => `${sub.account_id} ${sub.plan_name} ${sub.id}`.toLowerCase().includes(word));
  return { text, setText, standing, setStanding, query, loaded, rows, narrowed: Boolean(q) || standing !== "all" };
}

// The subscriptions tab: every account's subscriptions, found by account or plan, narrowed by whether they run, sortable by
// how much of the day's quota is used and by when they end; one that ends within a week is marked, as it does not renew
// itself. A row opens a drawer. A node whose list is not there yet (501) shows the tab as having none.
export function SubsTab() {
  const { t } = useTranslation();
  const format = useFormat();
  const list = useSubscriptionList();
  const [selected, select] = useSelected();
  const [now] = useState(() => Date.now());
  const open = list.loaded.find((sub) => sub.id === selected);
  const none = { title: t("subscriptions.noSubs.title"), body: t("subscriptions.noSubs.body") };
  return (
    <>
      <SummaryStrip />
      {isNotImplemented(list.query.error) ? (
        <EmptyState title={none.title}>{none.body}</EmptyState>
      ) : (
        <ListTable<Subscription, Standing>
          label={t("subscriptions.tabs.subs")}
          unit={t("subscriptions.unit")}
          search={{ label: t("subscriptions.search"), value: list.text, onChange: list.setText }}
          filter={{
            label: t("subscriptions.filter.label"),
            value: list.standing,
            options: standings.map((value) => ({ value, label: t(`subscriptions.filter.${value}`) })),
            onChange: list.setStanding,
          }}
          query={list.query}
          rows={list.rows}
          loaded={list.loaded.length}
          rowKey={(sub) => sub.id}
          selected={selected}
          onOpen={(sub) => select(sub.id)}
          narrowed={list.narrowed}
          empty={none}
          columns={subscriptionColumns(t, format, now)}
        />
      )}
      {open ? <SubscriptionDrawer sub={open} keys={list.rows.map((sub) => sub.id)} select={select} now={now} /> : null}
    </>
  );
}
