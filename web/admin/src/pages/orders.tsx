import type { AdminSchemas } from "@axiom/api";
import { useSession } from "@axiom/app";
import { DataState, PageBody, PageHead } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useLocation, useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import enOrders from "../locales/en/orders";
import enDiscrepancies from "../locales/en/payment-discrepancies";
import zhOrders from "../locales/zh/orders";
import zhDiscrepancies from "../locales/zh/payment-discrepancies";
import { DiscrepanciesTab } from "../orders/discrepancies-tab";
import { CashMovements, PaidBatches } from "../orders/ledger";
import { OrdersStrip } from "../orders/orders-strip";
import { OrdersTab } from "../orders/orders-tab";
import { RefundsTab } from "../orders/refunds-tab";
import { accountQuery } from "../queries";
import { RouteTabs } from "../row-drawer";

const paths = {
  "/orders": "orders",
  "/orders/cash": "cash",
  "/orders/batches": "batches",
  "/orders/refunds": "refunds",
  "/orders/discrepancies": "discrepancies",
} as const;
type Path = keyof typeof paths;

// 订单: what people paid and what was done with it, in five tabs: the orders, the cash movements behind them, the credit
// batches they made, the refunds asked for and the differences the channel's records have with ours. The old addresses,
// /orders?cash=1, /orders?paid=batches and /payment-discrepancies, lead to the tabs (router.tsx). The site is the admin
// account's, as on the settings page (the admin has no site list).
export default function Orders() {
  const { t } = useTranslation();
  const owner = useQuery(accountQuery(useSession().account));
  const path = useLocation({ select: (location) => location.pathname.replace(/\/$/, "") }) as Path;
  return (
    <PageBody>
      <PageHead title={t("shell.nav.orders")} />
      <RouteTabs
        label={t("orders.tabs.label")}
        current={path}
        tabs={(Object.keys(paths) as Path[]).map((key) => ({ key, to: key, label: t(`orders.tabs.${paths[key]}`) }))}
      />
      <DataState query={owner}>{(account) => <Current path={path} siteId={account.site_id} />}</DataState>
    </PageBody>
  );
}

function Current({ path, siteId }: { path: Path; siteId: AdminSchemas["SiteId"] }) {
  const { since, until } = useSearch({ strict: false }) as { since?: string; until?: string };
  switch (path) {
    case "/orders/cash":
      return <CashMovements siteId={siteId} since={since} until={until} />;
    case "/orders/batches":
      return <PaidBatches siteId={siteId} since={since} until={until} />;
    case "/orders/refunds":
      return <RefundsTab siteId={siteId} />;
    case "/orders/discrepancies":
      return <DiscrepanciesTab />;
    default:
      return (
        <>
          <OrdersStrip siteId={siteId} />
          <OrdersTab siteId={siteId} />
        </>
      );
  }
}

export const messages = {
  zh: { orders: zhOrders, discrepancies: zhDiscrepancies },
  en: { orders: enOrders, discrepancies: enDiscrepancies },
};
