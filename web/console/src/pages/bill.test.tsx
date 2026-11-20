// @vitest-environment jsdom
import { useFormat } from "@axiom/ui";
import { createInstance } from "i18next";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { I18nextProvider } from "react-i18next";
import { afterEach, describe, expect, it, vi } from "vitest";
import { answer, render, settle, submit } from "../testing";
import BalancePage from "./balance";
import {
  currencyInputPrefix,
  dailyNetChange,
  formatQuickAmount,
  projectedBalanceAfter,
  quickAmountsForCurrency,
  splitMoneyParts,
  totalFrozenFromBatches,
} from "./bill-data";
import {
  balance,
  buttonWithText,
  captureTimeoutThenSuccess,
  getMock,
  invalidStatementDay,
  makeTestOrder,
  navigateMock,
  postMock,
  setFieldValue,
} from "./bill-testing";
import OrdersPage from "./orders";
import RedeemPage from "./redeem";
import StatementPage from "./statement";

afterEach(() => vi.restoreAllMocks());

vi.mock("@axiom/api", async (original) => (await import("./bill-testing")).apiMock(original));
vi.mock("@axiom/app", async (original) => (await import("./bill-testing")).appMock(original));
vi.mock("@tanstack/react-router", async (original) => (await import("./bill-testing")).routerMock(original));

async function formatInLanguage(lng: "zh" | "en", read: (format: ReturnType<typeof useFormat>) => string) {
  const i18n = createInstance();
  await i18n.init({ lng, resources: {} });
  let output = "";
  function Probe() {
    output = read(useFormat("UTC"));
    return null;
  }
  const host = document.createElement("div");
  document.body.append(host);
  await act(async () => {
    createRoot(host).render(
      <I18nextProvider i18n={i18n}>
        <Probe />
      </I18nextProvider>,
    );
  });
  return output;
}

describe("billing integer-billionth math and currency formatting", () => {
  it("computes daily net, batch frozen total, and projected balance without float precision loss", () => {
    const net = dailyNetChange({
      day: "2026-10-14",
      topped_up: { amount: "9007199254740993", currency: "USD" },
      gifted: { amount: "2000000000", currency: "USD" },
      spent: { amount: "1", currency: "USD" },
      expired: { amount: "0", currency: "USD" },
      closing_balance: null,
    });
    expect(net).toBe("9007201254740992");

    expect(totalFrozenFromBatches(undefined)).toBeNull();
    expect(totalFrozenFromBatches([])).toBe("0");
    expect(
      totalFrozenFromBatches([
        {
          id: "b1",
          source: "paid",
          amount: { amount: "10000000000", currency: "USD" },
          remaining: { amount: "8000000000", currency: "USD" },
          frozen: { amount: "2000000000", currency: "USD" },
          created_at: "2026-10-01T00:00:00Z",
        },
        {
          id: "b2",
          source: "gift",
          amount: { amount: "5000000000", currency: "USD" },
          remaining: { amount: "5000000000", currency: "USD" },
          frozen: { amount: "1500000001", currency: "USD" },
          created_at: "2026-10-02T00:00:00Z",
        },
      ]),
    ).toBe("3500000001");

    expect(projectedBalanceAfter({ amount: "11750000000", currency: "USD" }, null)).toBeNull();
    expect(projectedBalanceAfter({ amount: "11750000000", currency: "USD" }, "0")).toBeNull();
    expect(projectedBalanceAfter({ amount: "9223372036854775806", currency: "USD" }, "1")).toEqual({
      amount: "9223372036854775807",
      currency: "USD",
    });
  });

  it("formats USD, CNY, JPY, and negative money in zh and en with exact digits", async () => {
    const usdZh = await formatInLanguage("zh", (f) => f.money({ amount: "11750000000", currency: "USD" }));
    const usdEn = await formatInLanguage("en", (f) => f.money({ amount: "11750000000", currency: "USD" }));
    expect(splitMoneyParts(usdZh)).toEqual({ currency: "US$", whole: "11", cents: ".75" });
    expect(splitMoneyParts(usdEn)).toEqual({ currency: "$", whole: "11", cents: ".75" });

    const exactCnyZh = await formatInLanguage("zh", (f) => f.money({ amount: "9007199254740993", currency: "CNY" }));
    const exactCnyEn = await formatInLanguage("en", (f) => f.money({ amount: "9007199254740993", currency: "CNY" }));
    expect(exactCnyZh).toContain("9,007,199.254740993");
    expect(exactCnyEn).toContain("9,007,199.254740993");

    const negUsdEn = await formatInLanguage("en", (f) => f.money({ amount: "-108830000000", currency: "USD" }));
    expect(splitMoneyParts(negUsdEn)).toEqual({ currency: "-$", whole: "108", cents: ".83" });

    const jpyPayEn = await formatInLanguage("en", (f) => f.pay({ amount: "1000", currency: "JPY" }));
    expect(jpyPayEn).toBe("¥1,000");

    const quickUsd = await formatInLanguage("en", (f) => formatQuickAmount("25", "USD", f.money));
    expect(quickUsd).toBe("$25");
    expect(quickAmountsForCurrency("USD")).toEqual(["10", "25", "50", "100"]);
    expect(quickAmountsForCurrency("CNY")).toEqual(["100", "200", "500", "1000"]);
    expect(currencyInputPrefix("CNY")).toBe("¥");
    expect(currencyInputPrefix("USD")).toBe("$");
  });
});

describe("daily statement unavailable balance and error states", () => {
  it("renders balanceUnknown instead of 0 when closing_balance is null, plus 404/400 states", async () => {
    getMock.mockImplementation((path: string) => {
      if (path === "/api/console/v1/statement") return answer(200, { data: [], next_cursor: null });
      if (path === "/api/console/v1/statement/daily") {
        return answer(200, {
          data: [
            {
              day: "2026-10-14",
              spent: { amount: "500000000", currency: "USD" },
              topped_up: { amount: "0", currency: "USD" },
              gifted: { amount: "0", currency: "USD" },
              expired: { amount: "0", currency: "USD" },
              closing_balance: null,
            },
            {
              day: "2026-10-13",
              spent: { amount: "0", currency: "USD" },
              topped_up: { amount: "10000000000", currency: "USD" },
              gifted: { amount: "0", currency: "USD" },
              expired: { amount: "0", currency: "USD" },
              closing_balance: { amount: "10000000000", currency: "USD" },
            },
          ],
          next_cursor: null,
        });
      }
      return answer(404, "not_found");
    });

    const { container } = await render(<StatementPage />);
    const dailyToggle = container.querySelector('input[type="radio"][value="daily"]') as HTMLInputElement;
    await act(async () => dailyToggle.click());
    await settle();

    const rows = Array.from(container.querySelectorAll("tbody tr"));
    expect(rows).toHaveLength(2);
    const cellsFirstDay = Array.from(rows[0]?.querySelectorAll("td") ?? []);
    expect(cellsFirstDay[2]?.textContent).toBe("statement.balanceUnknown");
    expect(cellsFirstDay[2]?.textContent).not.toBe("$0.00");
    expect(rows[1]?.querySelectorAll("td")[2]?.textContent).toBe("$10.00");

    getMock.mockImplementation((path: string) =>
      path === "/api/console/v1/statement/daily"
        ? invalidStatementDay("")
        : answer(200, { data: [], next_cursor: null }),
    );
    const second = await render(<StatementPage />);
    const secondToggle = second.container.querySelector('input[type="radio"][value="daily"]') as HTMLInputElement;
    await act(async () => secondToggle.click());
    await settle();
    expect(second.container.textContent).toContain("statement.bill.daily.tooMany");
  });
});

async function renderAndSubmitRedeem(code: string) {
  getMock.mockImplementation(() => answer(200, { data: [], next_cursor: null }));
  const rendered = await render(<RedeemPage />);
  const input = rendered.container.querySelector("input") as HTMLInputElement;
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
  await act(async () => {
    setValue?.call(input, code);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await submit(rendered.container.querySelector("form") as HTMLFormElement);
  return rendered.container;
}

describe("redeem code failure handling", () => {
  it("shows the refusal reason in an alert and preserves the typed code when invalid or used", async () => {
    postMock.mockImplementation(() => answer(422, "redeem_code_used"));
    const container = await renderAndSubmitRedeem("USED-CODE-2026");

    expect(container.querySelector('[role="alert"]')?.textContent).toContain("errors.redeem_code_used");
    expect((container.querySelector("input") as HTMLInputElement).value).toBe("USED-CODE-2026");
  });

  it("resends the stored redeem code with the same Idempotency-Key after a request timeout", async () => {
    const key = "11111111-1111-4111-8111-111111111111";
    vi.spyOn(crypto, "randomUUID").mockReturnValue(key);
    const keys: string[] = [];
    captureTimeoutThenSuccess(keys, {
      id: "red_01OK",
      code_hint: "2026",
      amount: { amount: "5000000000", currency: "USD" },
      redeemed_at: "2026-10-14T08:30:00Z",
    });

    const container = await renderAndSubmitRedeem("RETRY-2026");
    expect((container.querySelector("input") as HTMLInputElement).disabled).toBe(true);
    expect(container.textContent).toContain("redeem.resend");
    await submit(container.querySelector("form") as HTMLFormElement);

    expect(keys).toEqual([key, key]);
    expect(container.textContent).toContain("redeem.redeemed");
  });
});

function makePaidOrder(id: string, credits = "10000000000") {
  return {
    ...makeTestOrder(id, credits, "7200"),
    state: "paid" as const,
    paid_at: "2026-10-14T08:31:00Z",
    paid_total: { amount: "7200", currency: "CNY" },
    credited: { amount: credits, currency: "USD" },
  };
}

describe("refund dialog and balance paid-batch navigation", () => {
  it("persists unknown refund by order id across close/reopen and isolates interleaved orders A and B", async () => {
    const orderA = makePaidOrder("ord_01A", "100000000000");
    const orderB = makePaidOrder("ord_01B", "50000000000");
    getMock.mockImplementation((path: string) => {
      if (path === "/api/console/v1/orders") return answer(200, { data: [orderA, orderB], next_cursor: null });
      if (path === "/api/console/v1/balance") return answer(200, balance("10000000000"));
      return answer(200, { currency: "USD", name: "Axiom" });
    });
    let rejectA!: (reason?: unknown) => void;
    let resolveA!: (reply: Awaited<ReturnType<typeof answer>>) => void;
    const calls: { path: string; orderId?: string; key?: string; body?: unknown }[] = [];
    postMock.mockImplementation(
      (
        path: string,
        init: { params: { path?: { order_id?: string }; header: Record<string, string> }; body?: unknown },
      ) => {
        calls.push({
          path,
          orderId: init.params.path?.order_id,
          key: init.params.header["Idempotency-Key"],
          body: init.body,
        });
        if (calls.length === 1)
          return new Promise((_res, rej) => {
            rejectA = rej;
          });
        return new Promise((resolve) => {
          resolveA = resolve;
        });
      },
    );

    const { container } = await render(<OrdersPage />);
    const refundBtns = Array.from(container.querySelectorAll("button")).filter(
      (b) => b.textContent === "orders.refund",
    );
    await act(async () => refundBtns[0]?.click());
    const dialogA = container.querySelector("dialog") as HTMLDialogElement;
    await act(async () => {
      setFieldValue(dialogA.querySelector("input") as HTMLInputElement, "10");
      setFieldValue(dialogA.querySelector("textarea") as HTMLTextAreaElement, "reason A");
    });
    await act(async () => (dialogA.querySelector('button[type="submit"]') as HTMLButtonElement).click());
    await act(async () => buttonWithText(dialogA, "orders.close").click());
    await act(async () => refundBtns[1]?.click());
    const dialogB = container.querySelector("dialog") as HTMLDialogElement;
    await act(async () => rejectA(new DOMException("Timed out", "TimeoutError")));
    await settle();

    expect(container.querySelector("dialog")).toBe(dialogB);
    expect((dialogB.querySelector("input") as HTMLInputElement).value).toBe("50");
    expect((dialogB.querySelector("textarea") as HTMLTextAreaElement).value).toBe("");
    expect(sessionStorage.getItem("user_01TEST/acc_01TEST/axiom.console.refund/ord_01B")).toBeNull();
    const key = calls[0]?.key;
    expect(key).toBeTruthy();
    expect(sessionStorage.getItem("user_01TEST/acc_01TEST/axiom.console.refund/ord_01A")).toContain(key);

    await act(async () => buttonWithText(dialogB, "orders.close").click());
    await act(async () => refundBtns[0]?.click());
    const reopenedA = container.querySelector("dialog") as HTMLDialogElement;
    expect(reopenedA.textContent).toContain("orders.refundUnknown");
    await act(async () => buttonWithText(reopenedA, "orders.refundRetry").click());
    await settle();
    expect(calls[1]).toEqual({
      path: "/api/console/v1/orders/{order_id}/refunds",
      orderId: "ord_01A",
      key,
      body: { credits: { amount: "10000000000", currency: "USD" }, reason: "reason A" },
    });
    await act(async () => buttonWithText(reopenedA, "orders.close").click());
    await act(async () => refundBtns[1]?.click());
    const reopenedB = container.querySelector("dialog");
    await act(async () => resolveA(await answer(200, { id: "ref_1", state: "pending_review" })));
    await settle();
    expect(container.querySelector("dialog")).toBe(reopenedB);
    expect(sessionStorage.getItem("user_01TEST/acc_01TEST/axiom.console.refund/ord_01A")).toBeNull();
  });

  it("navigates paid batch rows on /balance to /orders rather than a batch UUID receipt path", async () => {
    getMock.mockImplementation((path: string) => {
      if (path === "/api/console/v1/balance") {
        return answer(200, {
          ...balance("10000000000"),
          batches: [
            {
              id: "00000000-0000-0000-0000-000000000001",
              source: "paid",
              amount: { amount: "10000000000", currency: "USD" },
              remaining: { amount: "10000000000", currency: "USD" },
              frozen: { amount: "0", currency: "USD" },
              created_at: "2026-10-14T08:30:00Z",
            },
          ],
        });
      }
      return answer(404, "not_found");
    });

    const { container } = await render(<BalancePage />);
    const paidRow = container.querySelector('tr[data-a="orders"]') as HTMLTableRowElement | null;
    expect(paidRow).not.toBeNull();
    await act(async () => paidRow?.click());
    expect(navigateMock).toHaveBeenCalledWith({ to: "/orders" });
  });
});
