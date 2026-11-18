// @vitest-environment jsdom
import { useFormat } from "@axiom/ui";
import { createInstance } from "i18next";
import { act, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { answer, render, settle, setUpPages, submit } from "../testing";
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
import { BillTopUpModal } from "./bill-topup";
import { RefundDialog } from "./orders-refund";
import RedeemPage from "./redeem";
import StatementPage from "./statement";

const { getMock, postMock, navigateMock } = vi.hoisted(() => {
  if (typeof globalThis.CSSStyleSheet === "undefined") {
    globalThis.CSSStyleSheet = class {
      cssRules: { style: { getPropertyValue: (n: string) => string } }[] = [{ style: { getPropertyValue: () => "" } }];
      insertRule() {
        return 0;
      }
    } as unknown as typeof CSSStyleSheet;
  }
  return { getMock: vi.fn(), postMock: vi.fn(), navigateMock: vi.fn() };
});

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>();
  return {
    ...actual,
    useLocation: () => ({ pathname: "/balance", hash: "" }),
    useNavigate: () => navigateMock,
    Link: ({ to, children, ...rest }: { to?: string; children?: ReactNode } & Record<string, unknown>) => (
      <a href={typeof to === "string" ? to : "#"} {...rest}>
        {children}
      </a>
    ),
  };
});

vi.mock("@axiom/app", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@axiom/app")>();
  return {
    ...actual,
    can: () => true,
    useSession: () => ({ user: { id: "user_01TEST" }, account: "acc_01TEST" }),
    useSessionBound: () => undefined,
  };
});

vi.mock("@axiom/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@axiom/api")>();
  return { ...actual, consoleApi: { ...actual.consoleApi, GET: getMock, POST: postMock } };
});

setUpPages();

beforeEach(() => {
  sessionStorage.clear();
  getMock.mockReset();
  postMock.mockReset();
  navigateMock.mockReset();
});

afterEach(() => {
  vi.restoreAllMocks();
});

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
    expect(quickUsd).toBe("$25.00");
    expect(quickAmountsForCurrency("USD")).toEqual(["10", "25", "50", "100"]);
    expect(quickAmountsForCurrency("CNY")).toEqual(["100", "200", "500", "1000"]);
    expect(currencyInputPrefix("CNY")).toBe("¥");
    expect(currencyInputPrefix("USD")).toBe("$");
  });
});

function buttonWithText(container: Element, label: string) {
  const found = Array.from(container.querySelectorAll("button")).find((btn) => btn.textContent?.includes(label));
  if (!found) throw new Error(`Missing button containing ${label}`);
  return found;
}

function makeTestOrder(id: string, creditsAmount: string, totalCny: string) {
  return {
    id,
    state: "pending" as const,
    method: "alipay",
    credits: { amount: creditsAmount, currency: "USD" },
    total: { amount: totalCny, currency: "CNY" },
    fee: { amount: "0", currency: "CNY" },
    created_at: "2026-10-14T08:30:00Z",
    expires_at: "2026-10-14T08:45:00Z",
    checkout: { pay_url: `https://pay.example.com/checkout/${id}` },
    refund_allowed: true,
    refunds: [],
  };
}

function setupTopUpGetMock(order: ReturnType<typeof makeTestOrder>, totalCny: string) {
  getMock.mockImplementation((path: string) => {
    if (path === "/api/console/v1/payment-methods") {
      return answer(200, {
        data: [
          { method: "alipay", total: { amount: totalCny, currency: "CNY" }, fee: { amount: "0", currency: "CNY" } },
        ],
      });
    }
    if (path === "/api/console/v1/orders/{order_id}") return answer(200, order);
    return answer(404, "not_found");
  });
}

function captureTimeoutThenSuccess(keys: string[], responsePayload: unknown) {
  return postMock
    .mockImplementationOnce((_path: string, init: { params: { header: Record<string, string> } }) => {
      keys.push(init.params.header["Idempotency-Key"] ?? "");
      return Promise.reject(new DOMException("The operation timed out.", "TimeoutError"));
    })
    .mockImplementationOnce((_path: string, init: { params: { header: Record<string, string> } }) => {
      keys.push(init.params.header["Idempotency-Key"] ?? "");
      return answer(200, responsePayload);
    });
}

describe("top-up idempotent retry on timeout", () => {
  it("reuses the same Idempotency-Key when retrying after a request timeout", async () => {
    const uuids = ["idem-order-key-1", "idem-order-key-2"];
    vi.spyOn(crypto, "randomUUID").mockImplementation(
      () => (uuids.shift() ?? "idem-fallback") as `${string}-${string}-${string}-${string}-${string}`,
    );
    const order = makeTestOrder("ord_01RETRY", "25000000000", "18000");
    setupTopUpGetMock(order, "18000");
    const keys: string[] = [];
    captureTimeoutThenSuccess(keys, order);

    const { container } = await render(
      <BillTopUpModal
        open
        currency="USD"
        currentBalance={{ amount: "11750000000", currency: "USD" }}
        onClose={() => {}}
      />,
    );
    const submitBtn = buttonWithText(container, "statement.bill.topUpModal.paySubmit");
    expect(submitBtn.disabled).toBe(false);
    await act(async () => submitBtn.click());
    await settle();

    expect(container.textContent).toContain("topup.unknown");
    expect(container.textContent).toContain("topup.retry");

    await act(async () => buttonWithText(container, "topup.retry").click());
    await settle();

    expect(keys).toEqual(["idem-order-key-1", "idem-order-key-1"]);
    expect(postMock).toHaveBeenCalledTimes(2);
    expect(container.querySelector("dialog")?.open).toBe(true);
  });

  it("recovers a persisted unknown attempt after reload, locks the amount input, and retries with the stored key", async () => {
    const storeKey = "user_01TEST/acc_01TEST/axiom.console.top-up.attempt";
    sessionStorage.setItem(
      storeKey,
      JSON.stringify({
        state: "unknown",
        key: "idem-reload-key",
        request: { method: "alipay", credits: { amount: "50000000000", currency: "USD" } },
      }),
    );
    const order = makeTestOrder("ord_01RELOAD", "50000000000", "36000");
    setupTopUpGetMock(order, "36000");
    const keys: string[] = [];
    postMock.mockImplementation((_path: string, init: { params: { header: Record<string, string> } }) => {
      keys.push(init.params.header["Idempotency-Key"] ?? "");
      return answer(200, order);
    });

    const { container } = await render(
      <BillTopUpModal
        open
        currency="USD"
        currentBalance={{ amount: "11750000000", currency: "USD" }}
        onClose={() => {}}
      />,
    );
    const amountInput = container.querySelector('input[inputmode="decimal"]') as HTMLInputElement;
    expect(amountInput.value).toBe("50");
    expect(amountInput.readOnly).toBe(true);

    await act(async () => buttonWithText(container, "topup.retry").click());
    await settle();
    expect(keys).toEqual(["idem-reload-key"]);
    expect(sessionStorage.getItem(storeKey)).toBeNull();
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
        ? answer(400, "too_many_transactions")
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

describe("refund dialog and balance paid-batch navigation", () => {
  it("preserves an unknown refund attempt when closing the dialog", async () => {
    const onClose = vi.fn();
    const onReset = vi.fn();
    const order = {
      id: "ord_01PAID",
      state: "paid" as const,
      method: "alipay",
      credits: { amount: "10000000000", currency: "USD" },
      total: { amount: "7200", currency: "CNY" },
      fee: { amount: "0", currency: "CNY" },
      created_at: "2026-10-14T08:30:00Z",
      expires_at: "2026-10-14T08:45:00Z",
      paid_at: "2026-10-14T08:31:00Z",
      paid_total: { amount: "7200", currency: "CNY" },
      credited: { amount: "10000000000", currency: "USD" },
      refund_allowed: true,
      refunds: [],
    };
    const { container } = await render(
      <RefundDialog
        order={order}
        attempt={{
          state: "unknown",
          key: "idem-refund-1",
          request: { credits: { amount: "10000000000", currency: "USD" }, reason: "unused" },
        }}
        onAttempt={() => {}}
        onReset={onReset}
        onClose={onClose}
        onSent={() => {}}
      />,
    );
    await act(async () => buttonWithText(container, "orders.close").click());
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(onReset).not.toHaveBeenCalled();
  });

  it("navigates paid batch rows on /balance to /orders rather than a batch UUID receipt path", async () => {
    getMock.mockImplementation((path: string) => {
      if (path === "/api/console/v1/balance") {
        return answer(200, {
          balance: { amount: "10000000000", currency: "USD" },
          available: { amount: "10000000000", currency: "USD" },
          reserved: { amount: "0", currency: "USD" },
          floor: { amount: "0", currency: "USD" },
          spent_this_month: { amount: "0", currency: "USD" },
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
