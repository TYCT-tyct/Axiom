import type { Schemas } from "@axiom/api";
import { Button, DataState, EmptyState, Icon, PageHeader, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { receiptQuery, siteQuery } from "../queries";
import { receiptState } from "../receipt";

// Receipt is one paid top-up as a printable page: the site's name, the order, when it was paid and in which zone the
// reader's clock shows that, what the channel took, what was credited, and the refunds that came after. The browser's
// print dialog saves it as a PDF, so the site keeps no file and needs no PDF library or CJK font of its own.
export default function Receipt() {
  const { t } = useTranslation();
  const { orderId } = useParams({ from: "/app/orders/$orderId/receipt" });
  const order = useQuery(receiptQuery(orderId));
  const site = useQuery(siteQuery);
  return (
    <>
      <PageHeader title={t("receipt.title")} />
      <div className="space-y-4 p-4 sm:p-6 print:p-0">
        <Button variant="ghost" asChild className="-ml-3 print:hidden">
          <Link to="/orders">
            <Icon name="left" />
            {t("receipt.back")}
          </Link>
        </Button>
        {/* The site's name is part of the receipt, so the order waits for it rather than printing a blank heading. */}
        <DataState query={site}>
          {(brand) => <DataState query={order}>{(data) => <ReceiptBody order={data} site={brand.name} />}</DataState>}
        </DataState>
      </div>
    </>
  );
}

// ReceiptBody is the document itself, or the sentence for an order that has none. It is separate from the page so a
// test can render a paid order, an unpaid one and one whose record is incomplete without a server.
export function ReceiptBody({ order, site }: { order: Schemas["Order"]; site: string }) {
  const { t } = useTranslation();
  const format = useFormat();
  const state = receiptState(order);
  if (state.kind !== "ready") {
    return (
      <EmptyState title={t(`receipt.none.${state.kind}.title`)}>{t(`receipt.none.${state.kind}.body`)}</EmptyState>
    );
  }
  // Only refunds that returned the money belong on a receipt: a declined or still running request moved nothing.
  const refunds = state.refunds.filter((refund) => refund.state === "succeeded");
  return (
    <>
      <div className="flex justify-end print:hidden">
        <Button variant="secondary" onClick={() => window.print()}>
          {t("receipt.print")}
        </Button>
      </div>
      <article className="mx-auto max-w-3xl rounded-card border border-b1 bg-s1 shadow-panel print:max-w-none print:rounded-none print:border-0 print:bg-bg print:shadow-none">
        <header className="border-b border-b1 px-6 py-5">
          <h2 className="text-lg text-t1">{site}</h2>
        </header>
        <dl className="divide-y divide-b1">
          <Row label={t("receipt.order")}>
            <span className="font-mono">{order.id}</span>
          </Row>
          <Row label={t("receipt.paidAt")}>{format.dateTime(state.paidAt)}</Row>
          <Row label={t("receipt.method")}>{order.method}</Row>
          <Row label={t("receipt.paid")}>
            <span className="tabular-nums">{format.pay(state.paid)}</span>
          </Row>
          <Row label={t("receipt.credited")}>
            <span className="tabular-nums">{format.money(state.credited)}</span>
          </Row>
        </dl>
        <section className="border-t border-b1 px-6 py-4">
          <h3 className="text-ui font-medium text-t1">{t("receipt.refunds")}</h3>
          {refunds.length === 0 ? (
            <p className="mt-1 text-ui text-t3">{t("receipt.noRefunds")}</p>
          ) : (
            <ul className="mt-1 divide-y divide-b1">
              {refunds.map((refund) => (
                <li key={refund.id} className="grid gap-x-4 gap-y-0.5 py-2 sm:grid-cols-[10rem_minmax(0,1fr)]">
                  <span className="text-t3">{format.dateTime(new Date(refund.finished_at ?? refund.created_at))}</span>
                  <span className="text-t1 tabular-nums">
                    {t("receipt.refundLine", {
                      amount: format.pay(refund.amount),
                      credits: format.money(refund.credits),
                    })}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </section>
        <footer className="border-t border-b1 px-6 py-3 text-label text-t3">{t("receipt.footnote", { site })}</footer>
      </article>
    </>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid gap-x-4 gap-y-0.5 px-6 py-3 sm:grid-cols-[10rem_minmax(0,1fr)]">
      <dt className="text-t3">{label}</dt>
      <dd className="min-w-0 text-t1 [overflow-wrap:anywhere]">{children}</dd>
    </div>
  );
}
