import { Button, Cluster, DataState, usePageTitle } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { receiptQuery, siteQuery } from "../queries";
import { BillShell } from "./bill-shell";
import { ReceiptBody } from "./receipt-body";

export default function ReceiptPage() {
  const { t } = useTranslation();
  const { orderId } = useParams({ strict: false }) as { orderId: string };
  const site = useQuery(siteQuery);
  const order = useQuery(receiptQuery(orderId));
  usePageTitle(t("receipt.title"));

  const actions = (
    <div data-print="hidden">
      <Cluster gap={8}>
        <Button asChild variant="ghost" size="sm">
          <Link to="/orders">{t("receipt.back")}</Link>
        </Button>
        <Button variant="secondary" size="sm" onClick={() => window.print()}>
          {t("receipt.print")}
        </Button>
      </Cluster>
    </div>
  );

  return (
    <BillShell activeTab="orders" title={t("receipt.title")} subtitle={t("orders.description")}>
      <DataState query={site}>
        {(brand) => (
          <DataState query={order}>
            {(data) => <ReceiptBody order={data} site={brand.name} actions={actions} />}
          </DataState>
        )}
      </DataState>
    </BillShell>
  );
}
