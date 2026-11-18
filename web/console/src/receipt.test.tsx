import type { Schemas } from "@axiom/api";
import { createInstance } from "i18next";
import { renderToStaticMarkup } from "react-dom/server";
import { I18nextProvider } from "react-i18next";
import { beforeAll, describe, expect, it } from "vitest";
import en from "./locales/en";
import { ReceiptBody } from "./pages/receipt-body";

const i18n = createInstance();

beforeAll(async () => {
  await i18n.init({ lng: "en", defaultNS: "console", resources: { en: { console: en } } });
});

const paidAt = "2026-06-15T12:00:00Z";

const order: Schemas["Order"] = {
  id: "ord_01ARZ3NDEKTSV4RRFFQ69G5FAV",
  state: "partially_refunded",
  method: "alipay",
  credits: { amount: "10000000000", currency: "USD" },
  total: { amount: "7373", currency: "CNY" },
  fee: { amount: "73", currency: "CNY" },
  created_at: "2026-06-15T11:58:00Z",
  expires_at: "2026-06-15T12:13:00Z",
  paid_at: paidAt,
  paid_total: { amount: "7373", currency: "CNY" },
  credited: { amount: "10000000000", currency: "USD" },
  refund_allowed: true,
  refunds: [
    {
      id: "rfd_01ARZ3NDEKTSV4RRFFQ69G5FAW",
      state: "succeeded",
      credits: { amount: "2500000000", currency: "USD" },
      amount: { amount: "2000", currency: "CNY" },
      created_at: "2026-06-16T08:00:00Z",
      finished_at: "2026-06-17T09:30:00Z",
    },
    {
      id: "rfd_01ARZ3NDEKTSV4RRFFQ69G5FAX",
      state: "declined",
      credits: { amount: "1000000000", currency: "USD" },
      amount: { amount: "500", currency: "CNY" },
      created_at: "2026-06-18T08:00:00Z",
    },
  ],
};

const render = (o: Schemas["Order"]) =>
  renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      <ReceiptBody order={o} site="Site Under Test" />
    </I18nextProvider>,
  );

describe("a receipt", () => {
  // The figures are checked by the digits they carry, not by the locale's layout: the time zone and currency layout belong
  // to Intl and the page language, and what the receipt must not do is show another order's or another figure's numbers.
  it("shows the paid order's own facts and the refunds that returned money", () => {
    const html = render(order);
    expect(html).toContain("Site Under Test");
    expect(html).toContain(order.id);
    expect(html).toContain("alipay");
    expect(html).toContain("73.73");
    expect(html).toContain("CN¥");
    expect(html).toContain("10.00");
    // A time in the reader's zone: the exact zone is the browser's, the instant is the order's.
    expect(html).toContain("2026");
    expect(html).toContain("20.00");
    expect(html).toContain("2.50");
    // A declined refund moved no money, so it is not part of the record of what was paid.
    expect(html).not.toContain("5.00");
  });

  it("has nothing for an order that never completed a payment", () => {
    const html = render({ ...order, state: "pending", paid_at: undefined, paid_total: undefined, credited: undefined });
    expect(html).toContain("No receipt yet");
    expect(html).not.toContain("Amount paid");
  });

  // A chargeback does not undo the payment: the receipt records what was paid and refunded and says nothing about the
  // dispute, whose outcome the balance and the statement carry (docs/domains/payments.md「收据与发票」).
  it("still has a receipt for a payment that was later charged back", () => {
    const html = render({ ...order, state: "charged_back" });
    expect(html).toContain("73.73");
    expect(html).toContain("2.50");
  });

  // A read that does not carry the refunds cannot say whether money came back, so it is no receipt at all rather than a
  // receipt claiming no refunds (a mutated order entry once answered without them).
  it("is not drawn from a record without the refunds", () => {
    const html = render({ ...order, refunds: undefined });
    expect(html).toContain("Receipts are unavailable");
    expect(html).not.toContain("Amount paid");
  });
});
