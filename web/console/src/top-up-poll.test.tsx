// @vitest-environment jsdom
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { createInstance } from "i18next";
import { act, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { balanceQuery } from "./queries";
import { TopUp } from "./top-up";

const reads = vi.hoisted(() => ({ order: 0, balance: 0 }));

vi.mock("@axiom/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@axiom/api")>();
  const money = (amount: string) => ({ amount, currency: "USD" });
  const order = (state: "pending" | "paid") => ({
    id: "ord_1",
    state,
    method: "wxpay",
    credits: money("5000000000"),
    total: { amount: "5000", currency: "CNY" },
    fee: { amount: "0", currency: "CNY" },
    created_at: "2026-10-28T00:00:00Z",
    expires_at: "2026-10-28T00:15:00Z",
    refund_allowed: false,
    refunds: [],
  });
  const balance = (amount: string) => ({
    available: money(amount),
    spent_this_month: money("0"),
    balance: money(amount),
    reserved: money("0"),
    floor: money("0"),
    batches: [],
  });
  const ok = (data: unknown) => Promise.resolve({ data, response: new Response(null, { status: 200 }) });
  return {
    ...actual,
    consoleApi: {
      ...actual.consoleApi,
      GET: vi.fn((path: string) => {
        switch (path) {
          case "/api/console/v1/orders/{order_id}":
            reads.order += 1;
            return ok(order(reads.order === 1 ? "pending" : "paid"));
          case "/api/console/v1/balance":
            reads.balance += 1;
            return ok(balance(reads.balance === 1 ? "1000000000" : "2000000000"));
          case "/api/console/v1/orders":
            return ok({ data: [], next_cursor: null });
          default:
            return Promise.reject(new Error(`unexpected GET ${path}`));
        }
      }),
    },
  };
});

vi.mock("@axiom/app", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@axiom/app")>();
  return {
    ...actual,
    can: () => true,
    useErrors: () => ({ message: () => "failed" }),
    useSession: () => ({}),
  };
});

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>();
  return {
    ...actual,
    Link: ({ to, children }: { to: string; children?: ReactNode }) => <a href={to}>{children}</a>,
  };
});

const i18n = createInstance();

// React 19 asks for this before act() runs outside a test renderer.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

beforeAll(async () => {
  await i18n.init({
    lng: "en",
    ns: ["console"],
    defaultNS: "console",
    resources: {
      en: {
        console: {
          topup: {
            title: "Top up",
            orders: "Orders",
            loading: "Reading the order",
            summary: "{{credits}} for {{total}}",
            waiting: "Pay on the channel's page",
            verify: "I have paid",
            cancel: "Cancel",
            expires: "Pay by {{at}}",
            paidBody: "The credits arrived: {{credits}}",
            seeStatement: "Statement",
            new: "Top up again",
          },
          orders: { state: { pending: "Pending", paid: "Paid" } },
        },
      },
    },
  });
});

// BalanceProbe shows the balance the page next to the top-up panel displays, so a refresh the poll triggers is what
// the assertion reads, not a call count.
function BalanceProbe() {
  const { data } = useQuery(balanceQuery);
  return <output data-testid="balance">{data?.available.amount ?? "reading"}</output>;
}

// flush advances the fake clock and lets the reads and React updates it starts finish.
const flush = (ms: number) =>
  act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });

// TopUp's poll is the page's own: the order is pending when the panel opens, its query refetches on the interval
// orderQuery sets, the answer moves it to paid, and the panel shows the credited state while the balance beside it is
// read again. Removing the poll or the invalidation leaves the pending panel and the old balance.
describe("TopUp's poll", () => {
  afterEach(() => {
    vi.useRealTimers();
    sessionStorage.clear();
  });

  it("shows a payment that arrived after the page was read, and refreshes the balance", async () => {
    vi.useFakeTimers();
    sessionStorage.setItem("axiom.console.top-up", "ord_1");
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } });
    const container = document.createElement("div");
    document.body.append(container);
    const root = createRoot(container);
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <I18nextProvider i18n={i18n}>
            <TopUp currency="USD" />
            <BalanceProbe />
          </I18nextProvider>
        </QueryClientProvider>,
      );
    });
    await flush(0);
    await flush(0);
    const text = () => container.textContent ?? "";
    expect(text()).toContain("Pending");
    expect(text()).toContain("I have paid");
    expect(container.querySelector('[data-testid="balance"]')?.textContent).toBe("1000000000");

    await flush(15_000);
    await flush(1);
    await flush(1);

    expect(text()).toContain("The credits arrived");
    expect(text()).not.toContain("I have paid");
    expect(container.querySelector('[data-testid="balance"]')?.textContent).toBe("2000000000");
    await act(async () => root.unmount());
    container.remove();
    client.clear();
  });
});
