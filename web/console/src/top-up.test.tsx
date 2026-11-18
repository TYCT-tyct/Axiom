import { createInstance } from "i18next";
import { renderToStaticMarkup } from "react-dom/server";
import { I18nextProvider } from "react-i18next";
import { beforeAll, describe, expect, it } from "vitest";
import { Checkout } from "./order-status";
import { balanceMoved, orderMoved } from "./pages/bill-topup-state";

const i18n = createInstance();

beforeAll(async () => {
  await i18n.init({
    lng: "en",
    defaultNS: "console",
    resources: {
      console: {
        translation: {
          topup: {
            qr: "Scan or copy this code to pay",
            qrHint: "The channel’s app takes the code above.",
            payNow: "Open payment page",
            returnHint: "You’ll come back to this page after paying.",
            secret: "Payment secret",
            secretHint: "Shown only now; the channel’s page needs it.",
          },
        },
      },
    },
  });
});

describe("Checkout", () => {
  it("draws a QR code when the channel returns only qrcode", () => {
    const html = renderToStaticMarkup(
      <I18nextProvider i18n={i18n}>
        <Checkout checkout={{ qr_code: "wxp://pay/token" }} />
      </I18nextProvider>,
    );
    expect(html).toContain("<svg");
    expect(html).toContain("<path");
    expect(html).toContain("wxp://pay/token");
    expect(html).not.toContain("Open payment page");
  });
});

it("opens a payment link with the site origin as its referrer and no opener", () => {
  const html = renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      <Checkout checkout={{ pay_url: "https://wx.tenpay.com/checkout" }} />
    </I18nextProvider>,
  );
  expect(html).toContain('rel="noopener"');
  expect(html).toContain('referrerPolicy="origin"');
  expect(html).not.toContain("noreferrer");
});

describe("balanceMoved", () => {
  it("refreshes when the poll confirms a payment, and on the first read of a paid order", () => {
    expect(balanceMoved("pending", "paid")).toBe(true);
    expect(balanceMoved("pending", "review")).toBe(true);
    expect(balanceMoved(undefined, "paid")).toBe(true);
    expect(balanceMoved(undefined, "pending")).toBe(false);
    expect(balanceMoved("paid", "paid")).toBe(false);
  });
});

describe("orderMoved", () => {
  it("treats the next order's first paid read as a change, not as the previous order's paid state", () => {
    expect(orderMoved({ id: "ord_A", state: "paid" }, "ord_B", "paid")).toBe(true);
    expect(orderMoved({ id: "ord_B", state: "pending" }, "ord_B", "paid")).toBe(true);
    expect(orderMoved({ id: "ord_B", state: "paid" }, "ord_B", "paid")).toBe(false);
  });
});
