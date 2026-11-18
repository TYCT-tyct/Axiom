import { LoadMore } from "@axiom/app";
import { Button, Card, Cluster, DataState, EmptyState, Stack, usePageTitle } from "@axiom/ui";
import type { InfiniteData, UseInfiniteQueryResult } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { BillRegion } from "./bill-region";

export type BillTab = "balance" | "plans" | "orders" | "statement" | "redeem";

const TABS: readonly { id: BillTab; to: "/balance" | "/orders" | "/statement" | "/redeem"; hash?: string }[] = [
  { id: "balance", to: "/balance" },
  { id: "plans", to: "/balance", hash: "plans" },
  { id: "orders", to: "/orders" },
  { id: "statement", to: "/statement" },
  { id: "redeem", to: "/redeem" },
];

export function BillTabs({ active }: { active: BillTab }) {
  const { t } = useTranslation();
  return (
    <nav aria-label={t("statement.bill.tabsLabel")} data-shell="tabs">
      <Cluster gap={8}>
        {TABS.map((item) => {
          const isCurrent = item.id === active;
          return (
            <Button key={item.id} asChild size="sm" variant={isCurrent ? "secondary" : "ghost"}>
              <Link to={item.to} hash={item.hash} aria-current={isCurrent ? "page" : undefined}>
                {t(`statement.bill.tabs.${item.id}`)}
              </Link>
            </Button>
          );
        })}
      </Cluster>
    </nav>
  );
}

const EMPTY_BILL_TEXT = {
  heroLabel: "",
  heroCurrency: "",
  heroWhole: "",
  heroCents: "",
  topUpLabel: "",
  tableTitle: "",
};

export function BillShell({
  activeTab,
  title,
  subtitle,
  children,
}: {
  activeTab: BillTab;
  title: string;
  subtitle: string;
  children: ReactNode;
}) {
  usePageTitle(title);
  return (
    <BillRegion
      tabs={<BillTabs active={activeTab} />}
      text={{ title, subtitle, ...EMPTY_BILL_TEXT }}
      values={{ unbuilt: true, hideHero: true, hideTable: true }}
      extra={children}
    />
  );
}

export function BillPagedList<Row>({
  query,
  title,
  lede,
  emptyTitle,
  emptyBody,
  emptyAction,
  segmentTitle,
  segmentBody,
  renderTable,
}: {
  query: UseInfiniteQueryResult<InfiniteData<{ data: readonly Row[] }>, unknown>;
  title: string;
  lede?: string;
  emptyTitle: string;
  emptyBody: string;
  emptyAction?: ReactNode;
  segmentTitle?: string;
  segmentBody?: string;
  renderTable: (rows: readonly Row[]) => ReactNode;
}) {
  return (
    <DataState query={query}>
      {(data) => {
        const rows = data.pages.flatMap((page) => page.data);
        if (rows.length === 0) {
          const isSegment = query.hasNextPage && Boolean(segmentTitle);
          return (
            <Stack gap={12}>
              <Card title={title} lede={lede}>
                <EmptyState
                  title={isSegment && segmentTitle ? segmentTitle : emptyTitle}
                  action={isSegment ? undefined : emptyAction}
                >
                  {isSegment ? (segmentBody ?? emptyBody) : emptyBody}
                </EmptyState>
              </Card>
              <LoadMore list={query} />
            </Stack>
          );
        }
        return (
          <Stack gap={12}>
            <Card title={title} lede={lede} layout="flush">
              {renderTable(rows)}
            </Card>
            <LoadMore list={query} />
          </Stack>
        );
      }}
    </DataState>
  );
}
