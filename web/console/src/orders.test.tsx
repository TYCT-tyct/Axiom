import type { Schemas } from "@axiom/api";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createInstance } from "i18next";
import { renderToStaticMarkup } from "react-dom/server";
import { I18nextProvider } from "react-i18next";
import { beforeAll, expect, it } from "vitest";
import { OrderState } from "./pages/orders-state";

const i18n = createInstance();

beforeAll(async () => {
  await i18n.init({
    lng: "en",
    defaultNS: "console",
    resources: {
      console: {
        translation: {
          orders: {
            pay: "Pay",
            state: {
              pending: "Pending",
            },
          },
        },
      },
    },
  });
});

it("opens the order-list payment link with the site origin as its referrer and no opener", () => {
  const order = {
    id: "ord_01ARZ3NDEKTSV4RRFFQ69G5FAV",
    state: "pending",
    method: "wxpay",
    credits: { amount: "10000000000", currency: "USD" },
    total: { amount: "7373", currency: "CNY" },
    fee: { amount: "73", currency: "CNY" },
    created_at: "2026-10-28T00:00:00Z",
    expires_at: "2026-10-28T00:15:00Z",
    checkout: { pay_url: "https://wx.tenpay.com/checkout" },
    refund_allowed: true,
    refunds: [],
  } as Schemas["Order"];
  const html = renderToStaticMarkup(
    <QueryClientProvider client={new QueryClient()}>
      <I18nextProvider i18n={i18n}>
        <OrderState order={order} allowed={false} onRefund={() => {}} />
      </I18nextProvider>
    </QueryClientProvider>,
  );
  expect(html).toContain('rel="noopener"');
  expect(html).toContain('referrerPolicy="origin"');
  expect(html).not.toContain("noreferrer");
});
