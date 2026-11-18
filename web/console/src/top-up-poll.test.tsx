// @vitest-environment jsdom
import { useInfiniteQuery } from "@tanstack/react-query";
import { act } from "react";
import { afterEach, expect, it, vi } from "vitest";
import BalancePage from "./pages/balance";
import { balance, buttonWithText, cny, getMock, usd } from "./pages/bill-testing";
import { ordersQuery } from "./queries";
import { answer, render, settle } from "./testing";

vi.mock("@axiom/api", async (original) => (await import("./pages/bill-testing")).apiMock(original));
vi.mock("@axiom/app", async (original) => (await import("./pages/bill-testing")).appMock(original));
vi.mock("@tanstack/react-router", async (original) => (await import("./pages/bill-testing")).routerMock(original));

function OrdersListProbe() {
  useInfiniteQuery(ordersQuery);
  return null;
}

afterEach(() => vi.useRealTimers());

it.each([false, true])("refreshes the production balance and order list after payment (open: %s)", async (open) => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  sessionStorage.setItem("user_01TEST/acc_01TEST/axiom.console.top-up", "ord_1");
  let paid = false;
  getMock.mockImplementation((path: string) => {
    if (path === "/api/console/v1/orders/{order_id}") {
      return answer(200, {
        id: "ord_1",
        state: paid ? "paid" : "pending",
        method: "wxpay",
        credits: usd("5000000000"),
        total: cny("5000"),
        fee: cny("0"),
        created_at: "2026-10-28T00:00:00Z",
        expires_at: "2026-10-28T00:15:00Z",
        refund_allowed: false,
        refunds: [],
      });
    }
    if (path === "/api/console/v1/balance") return answer(200, balance(paid ? "6000000000" : "1000000000"));
    return answer(200, { data: [], next_cursor: null });
  });
  const { container } = await render(
    <>
      <BalancePage />
      <OrdersListProbe />
    </>,
  );
  expect(container.textContent).toContain("$1.00");
  await act(async () => buttonWithText(container, "topup.title").click());
  expect(container.querySelector("dialog")?.textContent).toContain("topup.verify");
  if (!open) {
    await act(async () => container.querySelector("dialog")?.close());
    expect(container.querySelector("dialog")).toBeNull();
  }

  paid = true;
  await act(async () => vi.advanceTimersByTimeAsync(3000));
  await settle();
  await settle();
  expect(container.textContent).toContain("$6.00");
  expect(getMock.mock.calls.filter(([path]) => path === "/api/console/v1/orders")).toHaveLength(2);
  if (open) expect(container.querySelector("dialog")?.textContent).toContain("topup.paidBody");
});
