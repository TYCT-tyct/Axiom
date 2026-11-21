// @vitest-environment jsdom
import { act } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { en as billEn, zh as billZh } from "../locales/bill";
import { answer, render, settle, submit } from "../testing";
import BalancePage from "./balance";
import {
  balance,
  buttonWithText as btn,
  captureTimeoutThenSuccess,
  cny,
  getMock,
  invalidStatementDay,
  locationState,
  makeTestOrder,
  postMock,
  quote,
  setFieldValue as setValue,
  toggleDesktop,
  usd,
} from "./bill-testing";
import { BillTopUpModal } from "./bill-topup";
import { parsePositiveCredits, useBillTopUpSession } from "./bill-topup-state";
import OrdersPage from "./orders";
import RedeemPage from "./redeem";
import StatementPage from "./statement";

function TopUpTestView(props: Omit<React.ComponentProps<typeof BillTopUpModal>, "session">) {
  return <BillTopUpModal {...props} session={useBillTopUpSession()} />;
}

vi.mock("@axiom/api", async (original) => (await import("./bill-testing")).apiMock(original));
vi.mock("@axiom/app", async (original) => (await import("./bill-testing")).appMock(original));
vi.mock("@tanstack/react-router", async (original) => (await import("./bill-testing")).routerMock(original));

afterEach(() => vi.restoreAllMocks());

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
      <TopUpTestView
        open
        currency="USD"
        currentBalance={{ amount: "11750000000", currency: "USD" }}
        onClose={() => {}}
      />,
    );
    const submitBtn = btn(container, "statement.bill.topUpModal.paySubmit");
    expect(submitBtn.disabled).toBe(false);
    await act(async () => submitBtn.click());
    await settle();

    expect(container.textContent).toContain("topup.unknown");
    expect(container.textContent).toContain("topup.retry");

    await act(async () => btn(container, "topup.retry").click());
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
      <TopUpTestView
        open
        currency="USD"
        currentBalance={{ amount: "11750000000", currency: "USD" }}
        onClose={() => {}}
      />,
    );
    const amountInput = container.querySelector('input[inputmode="decimal"]') as HTMLInputElement;
    expect(amountInput.value).toBe("50");
    expect(amountInput.readOnly).toBe(true);

    await act(async () => btn(container, "topup.retry").click());
    await settle();
    expect(keys).toEqual(["idem-reload-key"]);
    expect(sessionStorage.getItem(storeKey)).toBeNull();
  });
});

describe("top-up decimal precision, in-flight locks, 4xx preservation, and retry quote isolation", () => {
  it("accepts 3 and 9 decimals while rejecting 10 decimals, and updates precision copy", async () => {
    expect(parsePositiveCredits("1.001")).toBe("1001000000");
    expect(parsePositiveCredits("1.123456789")).toBe("1123456789");
    expect(parsePositiveCredits("1.1234567891")).toBeNull();
    expect(billZh.topUpModal.amountInvalid).toContain("九位小数");
    expect(billEn.topUpModal.amountInvalid).toContain("9 decimal places");

    getMock.mockImplementation(() => answer(200, { data: [] }));
    const { container } = await render(
      <TopUpTestView open currency="USD" currentBalance={{ amount: "0", currency: "USD" }} onClose={() => {}} />,
    );
    const input = container.querySelector('input[inputmode="decimal"]') as HTMLInputElement;
    await act(async () => setValue(input, "1.123456789"));
    expect(container.textContent).not.toContain("statement.bill.topUpModal.amountInvalid");
    await act(async () => setValue(input, "1.1234567891"));
    expect(container.textContent).toContain("statement.bill.topUpModal.amountInvalid");
  });

  it("restores the amount and method when the rejection arrived with the dialog closed", async () => {
    getMock.mockImplementation((path: string) =>
      path === "/api/console/v1/balance"
        ? answer(200, balance("10000000000"))
        : answer(200, { data: [quote("alipay", "18000"), quote("wxpay", "18100", "100")] }),
    );
    let resolveFirst!: (val: Awaited<ReturnType<typeof answer>>) => void;
    postMock.mockImplementationOnce(
      () =>
        new Promise((res) => {
          resolveFirst = res;
        }),
    );

    const { container } = await render(<BalancePage />);
    const quick = container.querySelector('button[data-amt="25"]') as HTMLButtonElement;
    const open = () => act(async () => quick.click());
    const amount = () => container.querySelector('input[inputmode="decimal"]') as HTMLInputElement;
    await open();
    const input = amount();
    expect(input.value).toBe("25");
    await act(async () => setValue(input, "10"));
    await settle();
    await act(async () => (container.querySelector('button[data-m="wxpay"]') as HTMLButtonElement).click());
    await act(async () => btn(container, "statement.bill.topUpModal.paySubmit").click());
    await settle();

    expect(container.textContent).not.toContain("topup.new");
    expect(input.readOnly).toBe(true);
    await act(async () => container.querySelector("dialog")?.close());
    await act(async () => resolveFirst(await answer(422, "invalid_request")));
    await settle();
    await open();
    await settle();

    expect(amount().value).toBe("10");
    expect(container.querySelector('button[data-m="wxpay"]')?.className).toContain("pym on");

    postMock.mockImplementationOnce(() => answer(422, "invalid_request"));
    await act(async () => btn(container, "statement.bill.topUpModal.paySubmit").click());
    const [first, second] = postMock.mock.calls;
    expect(second?.[1].body).toEqual({ method: "wxpay", credits: usd("10000000000") });
    expect(second?.[1].params.header["Idempotency-Key"]).not.toBe(first?.[1].params.header["Idempotency-Key"]);

    await settle();
    await act(async () => container.querySelector("dialog")?.close());
    getMock.mockResolvedValue(await answer(200, { data: [quote("alipay", "18000")] }));
    await open();
    await settle();
    expect(container.textContent).toContain("statement.bill.topUpModal.chooseMethod");
    expect(btn(container, "topup.submit").disabled).toBe(true);
    expect(postMock).toHaveBeenCalledTimes(2);
  });

  it("keeps the sent method and amount without a new quote after a timeout", async () => {
    getMock.mockImplementation(() => answer(200, { data: [quote("alipay", "7200"), quote("wxpay", "7300", "100")] }));
    let finishRetry!: (reply: Awaited<ReturnType<typeof answer>>) => void;
    postMock.mockRejectedValueOnce(new DOMException("timeout", "TimeoutError")).mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finishRetry = resolve;
        }),
    );
    const { container } = await render(
      <TopUpTestView open currency="USD" currentBalance={usd("10000000000")} onClose={() => {}} />,
    );
    await act(async () => btn(container, "statement.bill.topUpModal.paySubmit").click());
    await settle();
    getMock.mockImplementation(() => answer(200, { data: [quote("wxpay", "9999", "99")] }));
    expect(container.textContent).toContain("topup.retrySummary");
    expect(container.querySelector("button[data-m]")).toBeNull();

    await act(async () => btn(container, "topup.retry").click());
    await settle();
    expect(container.textContent).not.toContain("topup.new");
    expect(postMock.mock.calls[1]).toEqual(postMock.mock.calls[0]);
    expect(postMock.mock.calls[1]?.[1].body.method).toBe("alipay");
    await act(async () => finishRetry(await answer(422, "invalid_request")));
  });
});

describe("balance preview, missing balance/currency waiting, and orders/balance consistency", () => {
  it("computes afterBalance from total balance on /balance and marks preview unavailable when balance fails on /orders", async () => {
    getMock.mockImplementation((path: string) => {
      if (path === "/api/console/v1/balance") {
        return answer(200, balance("100000000000", "20000000000"));
      }
      if (path === "/api/console/v1/payment-methods") {
        return answer(200, {
          data: [quote("alipay", "7200")],
        });
      }
      return answer(404, "not_found");
    });

    const balView = await render(<BalancePage />);
    await act(async () => (balView.container.querySelector('button[data-amt="10"]') as HTMLButtonElement).click());
    await settle();
    expect(balView.container.querySelector(".pynb")?.textContent).toBe("$110.00");

    getMock.mockImplementation((path: string) => {
      if (path === "/api/console/v1/orders") return answer(200, { data: [], next_cursor: null });
      if (path === "/api/console/v1/site") return answer(200, { name: "Axiom", currency: "CNY" });
      if (path === "/api/console/v1/payment-methods") return answer(200, { data: [] });
      return answer(500, "internal_error");
    });

    const ordView = await render(<OrdersPage />);
    await act(async () => btn(ordView.container, "orders.empty.topUp").click());
    await settle();
    expect(ordView.container.querySelector(".pyamt > span")?.textContent).toBe("¥");
    expect(ordView.container.querySelector(".pynb")?.textContent).toBe("orders.unavailable");
  });

  it("waits for site or balance currency on #plans instead of defaulting to USD", async () => {
    locationState.hash = "#plans";
    let resolveSite!: (val: Awaited<ReturnType<typeof answer>>) => void;
    getMock.mockImplementation((path: string) => {
      if (path === "/api/console/v1/subscriptions") {
        return answer(200, {
          data: [
            {
              id: "sub_1",
              plan_name: "Pro",
              state: "active",
              starts_at: "2026-10-01T00:00:00Z",
              ends_at: "2026-11-01T00:00:00Z",
              current_window: {
                seq: 1,
                starts_at: "2026-10-01T00:00:00Z",
                ends_at: "2026-11-01T00:00:00Z",
                quota: "10000000000",
                used: "0",
                reserved: "0",
                remaining: "10000000000",
              },
            },
          ],
        });
      }
      if (path === "/api/console/v1/site")
        return new Promise((res) => {
          resolveSite = res;
        });
      if (path === "/api/console/v1/balance") return answer(500, "internal_error");
      return answer(200, { preference: "subscription_first", source: "site", site_default: "subscription_first" });
    });

    const { container } = await render(<BalancePage />);
    expect(container.textContent).not.toContain("USD");
    expect(container.textContent).not.toContain("$10.00");

    await act(async () => resolveSite(await answer(200, { name: "Axiom", currency: "CNY" })));
    await settle();
    expect(container.textContent).toContain("CN¥10.00");
    expect(container.textContent).toContain("settings.billing.hint");
    expect(container.textContent).not.toContain("settings.description");
  });
});

describe("daily statement signed buckets, zero-net wording, timezone errors, redeem protection & breakpoint preservation", () => {
  it("renders signed negative topped_up/spent and zero-net wording, and distinguishes time_zone vs ledger budget 400", async () => {
    getMock.mockImplementation((path: string) => {
      if (path === "/api/console/v1/statement/daily") {
        return answer(200, {
          data: [
            {
              day: "2026-10-15",
              spent: { amount: "-2000000000", currency: "USD" },
              topped_up: { amount: "-10000000000", currency: "USD" },
              gifted: { amount: "0", currency: "USD" },
              expired: { amount: "0", currency: "USD" },
              closing_balance: null,
            },
            {
              day: "2026-10-14",
              spent: { amount: "0", currency: "USD" },
              topped_up: { amount: "0", currency: "USD" },
              gifted: { amount: "0", currency: "USD" },
              expired: { amount: "0", currency: "USD" },
              closing_balance: { amount: "5000000000", currency: "USD" },
            },
          ],
          next_cursor: null,
        });
      }
      return answer(200, { data: [], next_cursor: null });
    });
    const st1 = await render(<StatementPage />);
    await act(async () => (st1.container.querySelector('input[value="daily"]') as HTMLInputElement).click());
    await settle();
    const rows = st1.container.querySelectorAll("tbody tr");
    expect(rows[0]?.textContent).toContain("statement.bill.daily.toppedUp");
    expect(rows[0]?.textContent).toContain("statement.bill.daily.spent");
    expect(rows[1]?.textContent).toContain("statement.bill.daily.zeroNet");
    expect(rows[1]?.textContent).not.toContain("noActivity");

    getMock.mockImplementation((path: string) =>
      path === "/api/console/v1/statement/daily"
        ? invalidStatementDay("time_zone")
        : answer(200, { data: [], next_cursor: null }),
    );
    const st2 = await render(<StatementPage />);
    await act(async () => (st2.container.querySelector('input[value="daily"]') as HTMLInputElement).click());
    await settle();
    expect(st2.container.textContent).toContain("statement.bill.daily.badTimeZone");
  });

  it("protects the redeem code and keeps the input across width changes", async () => {
    getMock.mockImplementation(() => answer(200, { data: [], next_cursor: null }));
    const redeemView = await render(<RedeemPage />);
    const redeemInput = redeemView.container.querySelector("input") as HTMLInputElement;
    expect(redeemInput.getAttribute("spellcheck")).toBe("false");
    expect(redeemInput.getAttribute("autocomplete")).toBe("off");
    expect(redeemInput.maxLength).toBe(64);
    await act(async () => setValue(redeemInput, "KEEP-ACROSS-WIDTH"));
    await act(async () => toggleDesktop(false));
    await act(async () => toggleDesktop(true));
    expect((redeemView.container.querySelector("input") as HTMLInputElement).value).toBe("KEEP-ACROSS-WIDTH");
  });

  it("keeps the one-time checkout credential across width changes and reopening", async () => {
    const pendingOrder = {
      id: "ord_sec",
      state: "pending",
      method: "alipay",
      credits: usd("25000000000"),
      total: cny("7200"),
      fee: cny("0"),
      created_at: "2026-10-14T08:30:00Z",
      expires_at: "2026-10-14T08:45:00Z",
      refund_allowed: false,
      refunds: [],
    };
    getMock.mockImplementation((path: string) => {
      switch (path) {
        case "/api/console/v1/orders":
          return answer(200, { data: [], next_cursor: null });
        case "/api/console/v1/balance":
          return answer(200, balance("10000000000"));
        case "/api/console/v1/payment-methods":
          return answer(200, { data: [quote("alipay", "7200")] });
        case "/api/console/v1/orders/{order_id}":
          return answer(200, pendingOrder);
        default:
          return answer(200, { name: "Axiom", currency: "USD" });
      }
    });
    postMock.mockImplementation(() =>
      answer(200, { ...pendingOrder, checkout: { client_secret: "secret_only_once_42" } }),
    );
    const ordView = await render(<OrdersPage />);
    await act(async () => btn(ordView.container, "orders.empty.topUp").click());
    await settle();
    await act(async () => btn(ordView.container, "statement.bill.topUpModal.paySubmit").click());
    await settle();
    expect(ordView.container.textContent).toContain("secret_only_once_42");
    const stepTwoClose = ordView.container.querySelector(
      'dialog button[aria-label="dialog.close"]',
    ) as HTMLButtonElement | null;
    expect(stepTwoClose).not.toBeNull();
    if (stepTwoClose) await act(async () => stepTwoClose.click());
    await act(async () => toggleDesktop(false));
    await act(async () => toggleDesktop(true));
    await act(async () => btn(ordView.container, "orders.empty.topUp").click());
    await settle();
    expect(ordView.container.textContent).toContain("secret_only_once_42");
    expect(sessionStorage.getItem("user_01TEST/acc_01TEST/axiom.console.top-up")).toBe("ord_sec");
  });

  it("preserves refund amount and reason on 4xx rejection", async () => {
    const order = {
      id: "ord_ref4xx",
      state: "paid",
      method: "alipay",
      credits: { amount: "100000000000", currency: "USD" },
      total: { amount: "7200", currency: "CNY" },
      fee: { amount: "0", currency: "CNY" },
      created_at: "2026-10-14T08:30:00Z",
      expires_at: "2026-10-14T08:45:00Z",
      paid_at: "2026-10-14T08:31:00Z",
      paid_total: { amount: "7200", currency: "CNY" },
      credited: { amount: "100000000000", currency: "USD" },
      refund_allowed: true,
      refunds: [],
    };
    getMock.mockImplementation((path: string) =>
      path === "/api/console/v1/orders"
        ? answer(200, { data: [order], next_cursor: null })
        : answer(200, {
            currency: "USD",
            name: "Axiom",
            ...balance("100000000000"),
          }),
    );
    postMock.mockImplementation(() => answer(422, "refund_too_large"));
    const { container } = await render(<OrdersPage />);
    await act(async () => btn(container, "orders.refund").click());
    const dialog = container.querySelector("dialog") as HTMLDialogElement;
    await act(async () => {
      setValue(dialog.querySelector("input") as HTMLInputElement, "10");
      setValue(dialog.querySelector("textarea") as HTMLTextAreaElement, "partial unused");
    });
    await submit(dialog.querySelector("form") as HTMLFormElement);
    expect((dialog.querySelector("input") as HTMLInputElement).value).toBe("10");
    expect((dialog.querySelector("textarea") as HTMLTextAreaElement).value).toBe("partial unused");
  });

  it("blocks redeem reset during in-flight submit/resend", async () => {
    getMock.mockImplementation(() => answer(200, { data: [], next_cursor: null }));
    let rejectFirst!: (err?: unknown) => void;
    let finishSecond!: () => void;
    postMock
      .mockImplementationOnce(
        () =>
          new Promise((_res, rej) => {
            rejectFirst = rej;
          }),
      )
      .mockImplementationOnce(
        () =>
          new Promise<void>((res) => {
            finishSecond = res;
          }),
      );

    const { container } = await render(<RedeemPage />);
    const input = container.querySelector("input") as HTMLInputElement;
    await act(async () => setValue(input, "INFLIGHT-1"));
    await submit(container.querySelector("form") as HTMLFormElement);

    expect(container.textContent).not.toContain("redeem.restart");
    expect(input.disabled).toBe(true);

    await act(async () => rejectFirst(new DOMException("timeout", "TimeoutError")));
    await settle();
    expect(container.textContent).toContain("redeem.restart");

    await submit(container.querySelector("form") as HTMLFormElement);
    expect((btn(container, "redeem.restart") as HTMLButtonElement).disabled).toBe(true);
    await act(async () => finishSecond());
    await settle();
  });
});
