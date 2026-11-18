// @vitest-environment jsdom
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { act } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { answer, render, settle } from "../testing";
import BalancePage from "./balance";
import {
  balance,
  buttonWithText as button,
  getMock,
  makeTestOrder,
  postMock,
  quote,
  setFieldValue,
  usd,
} from "./bill-testing";
import OrdersPage from "./orders";
import RedeemPage from "./redeem";

vi.mock("@axiom/api", async (original) => (await import("./bill-testing")).apiMock(original));
vi.mock("@axiom/app", async (original) => (await import("./bill-testing")).appMock(original));

afterEach(() => vi.restoreAllMocks());

async function openBilling(initial: "/balance" | "/redeem") {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  const root = createRootRoute({ component: Outlet });
  const routes = ["/balance", "/orders", "/redeem"] as const;
  const components = [BalancePage, OrdersPage, RedeemPage];
  const children = routes.map((path, index) =>
    createRoute({ getParentRoute: () => root, path, component: components[index] }),
  );
  const router = createRouter({
    routeTree: root.addChildren(children),
    history: createMemoryHistory({ initialEntries: [initial] }),
  });
  getMock.mockImplementation((path: string) => {
    if (path === "/api/console/v1/balance") return answer(200, balance("10000000000"));
    if (path === "/api/console/v1/payment-methods") return answer(200, { data: [quote("alipay", "7200")] });
    if (path === "/api/console/v1/orders/{order_id}") return answer(200, makeTestOrder("ord_1", "25000000000", "7200"));
    return answer(200, { currency: "USD", data: [], next_cursor: null });
  });
  const { container } = await render(<RouterProvider router={router} />);
  const navigate = async (to: (typeof routes)[number]) => {
    await act(async () => {
      await router.navigate({ to });
    });
    await settle();
  };
  return { container, navigate };
}

function holdRequest() {
  let finish!: (reply: Awaited<ReturnType<typeof answer>>) => void;
  postMock.mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  return (reply: ReturnType<typeof answer>) => act(async () => finish(await reply));
}

it("blocks another top-up across balance and orders until the first request settles", async () => {
  const reply = holdRequest();
  const { container, navigate } = await openBilling("/balance");
  await act(async () => button(container, "topup.title").click());
  await settle();
  await act(async () => button(container, "statement.bill.topUpModal.paySubmit").click());
  expect(postMock).toHaveBeenCalledTimes(1);
  await act(async () => container.querySelector("dialog")?.close());

  await navigate("/orders");
  await act(async () => button(container, "orders.empty.topUp").click());
  await settle();
  const dialog = container.querySelector("dialog") as HTMLDialogElement;
  expect(dialog.open).toBe(true);
  expect(dialog.textContent).not.toContain("topup.new");
  const submit = button(dialog, "topup.submit");
  expect(submit.disabled).toBe(true);
  submit.click();
  expect(postMock).toHaveBeenCalledTimes(1);

  const order = makeTestOrder("ord_1", "25000000000", "7200");
  await reply(answer(200, { ...order, checkout: { client_secret: "create-response-only" } }));
  await settle();
  expect(container.querySelector("dialog")?.textContent).toContain("create-response-only");
});

it("blocks starting another redemption after leaving and returning during a pending POST", async () => {
  const reply = holdRequest();
  const { container, navigate } = await openBilling("/redeem");
  const form = () => container.querySelector("form") as HTMLFormElement;
  await act(async () => {
    setFieldValue(container.querySelector("input") as HTMLInputElement, "REDEEM-2026");
  });
  await act(async () => form().requestSubmit());
  expect(postMock).toHaveBeenCalledTimes(1);

  await navigate("/balance");
  await navigate("/redeem");
  expect((form().querySelector("input") as HTMLInputElement).disabled).toBe(true);
  expect(Array.from(form().querySelectorAll("button")).every((node) => node.disabled)).toBe(true);
  await act(async () => form().requestSubmit());
  expect(postMock).toHaveBeenCalledTimes(1);

  await reply(
    answer(200, {
      id: "red_1",
      code_hint: "2026",
      amount: usd("5000000000"),
      redeemed_at: "2026-10-14T08:30:00Z",
    }),
  );
  await settle();
  expect(container.textContent).toContain("redeem.redeemedBody");
});
