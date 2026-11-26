import { can, useSession } from "@axiom/app";
import { Card, type RowItem, RowList, Text, useFormat } from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { byUrgency, type Todo } from "./dashboard";
import { discrepanciesQuery } from "./finance-queries";
import { alertEventsQuery } from "./policy-queries";
import { accountQuery, approvalsQuery, paymentRefundsQuery, pendingMappingsQuery, publicationsQuery } from "./queries";

// How many of what needs doing the card lists before "see N more" (the board shows five).
const shown = 5;

type Item = RowItem & Todo;
type Page<T> = { pages: { data?: T[] | null }[] } | undefined;
const firstPage = <T,>(data: Page<T>) => data?.pages[0]?.data ?? [];

// What waits for a person, read from the lists the pages behind it show: proposals to decide, alerts not resolved,
// refunds waiting for a review, differences with the payment channel, upstream models not yet tied to the catalog, and
// new sale prices not yet published. A list the session may not read, or that has not answered, adds nothing; each is
// its first page only, as the sidebar's counts are.
function useWaiting() {
  const session = useSession();
  const manage = can(session, "sites.manage");
  const owner = useQuery({ ...accountQuery(session.account), enabled: manage });
  const site = owner.data?.site_id;
  const publications = useQuery({ ...publicationsQuery, enabled: can(session, "pricing.view") });
  return {
    approvals: firstPage(
      useInfiniteQuery({ ...approvalsQuery("pending"), enabled: can(session, "operations.approve") }).data,
    ),
    alerts: firstPage(useInfiniteQuery({ ...alertEventsQuery, enabled: manage }).data).filter(
      (event) => event.state !== "resolved",
    ),
    refunds: firstPage(useInfiniteQuery({ ...paymentRefundsQuery(site ?? "", "frozen"), enabled: Boolean(site) }).data)
      .length,
    differences: firstPage(useInfiniteQuery({ ...discrepanciesQuery("open"), enabled: manage }).data).length,
    mappings: firstPage(useInfiniteQuery({ ...pendingMappingsQuery(), enabled: can(session, "suppliers.view") }).data)
      .length,
    unpublished: (publications.data?.data ?? []).filter(
      (publication) => publication.status === "pending" || publication.status === "blocked",
    ).length,
  };
}

type Waiting = ReturnType<typeof useWaiting>;
type Kind = "approvals" | "refunds" | "differences" | "mappings" | "prices";
type Words = { t: TFunction<"dashboard">; hour: (at: Date) => string; number: (n: number) => string };

// The rows of what waits, most pressing first (adTodo). A kind with nothing waiting has no row.
function todoOf(waiting: Waiting, { t, hour, number }: Words): Item[] {
  const counted = (kind: Kind, count: number) => t(`todo.${kind}`, { count, n: number(count) });
  const items: Item[] = waiting.alerts.map((event) => ({
    key: `alert:${event.id}`,
    rank: event.severity === "critical" ? 0 : 2,
    tone: event.severity === "critical" ? "bad" : "warn",
    title: event.kind ?? "",
    sub: event.key,
    link: <Link to="/alert-events" search={{ event: event.id ?? "" }} />,
  }));
  if (waiting.approvals.length > 0) {
    const first = Math.min(...waiting.approvals.map((approval) => Date.parse(approval.expires_at)));
    items.push({
      key: "approvals",
      rank: 1,
      title: counted("approvals", waiting.approvals.length),
      sub: t("todo.approvalsSub", { time: hour(new Date(first)) }),
      link: <Link to="/approvals" search={{ state: undefined }} />,
    });
  }
  const rest: [Kind, number, Item["link"], string?][] = [
    ["refunds", waiting.refunds, <Link key="r" to="/orders" search={{}} />],
    ["differences", waiting.differences, <Link key="d" to="/payment-discrepancies" search={{ state: "open" }} />],
    ["mappings", waiting.mappings, <Link key="m" to="/suppliers" search={{ tab: "inbox" }} />, t("todo.mappingsSub")],
    ["prices", waiting.unpublished, <Link key="p" to="/pricing" />],
  ];
  for (const [key, count, link, sub] of rest) {
    if (count > 0) items.push({ key, rank: 3, title: counted(key, count), sub, link });
  }
  return byUrgency(items);
}

export function TodoCard() {
  const { t } = useTranslation("dashboard");
  const format = useFormat();
  const items = todoOf(useWaiting(), { t, hour: format.hour, number: format.number });
  const [all, setAll] = useState(false);
  return (
    <Card
      layout="flush"
      title={t("todo.title")}
      actions={
        items.length > 0 ? (
          <Text as="span" size="sm" tone="faint" numeric>
            {t("todo.count", { count: items.length })}
          </Text>
        ) : undefined
      }
    >
      <RowList
        label={t("todo.title")}
        items={all ? items : items.slice(0, shown)}
        empty={t("todo.none")}
        alignTop
        more={
          items.length > shown
            ? {
                label: all ? t("todo.fewer") : t("todo.more", { count: items.length - shown }),
                onPress: () => setAll(!all),
              }
            : undefined
        }
      />
    </Card>
  );
}
