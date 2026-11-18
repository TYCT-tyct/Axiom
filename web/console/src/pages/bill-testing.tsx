import type { ReactNode } from "react";
import { beforeEach, vi } from "vitest";
import { answer, setUpPages } from "../testing";

if (typeof globalThis.CSSStyleSheet === "undefined") {
  globalThis.CSSStyleSheet = class {
    cssRules: { style: { getPropertyValue: (n: string) => string } }[] = [{ style: { getPropertyValue: () => "" } }];
    insertRule() {
      return 0;
    }
  } as unknown as typeof CSSStyleSheet;
}

export const getMock = vi.fn();
export const postMock = vi.fn();
export const navigateMock = vi.fn();
export const locationState = { pathname: "/balance", hash: "" };
const mediaState = { desktop: true, listeners: new Set<() => void>() };

export async function apiMock(original: () => Promise<typeof import("@axiom/api")>) {
  const actual = await original();
  return { ...actual, consoleApi: { ...actual.consoleApi, GET: getMock, POST: postMock } };
}

export async function appMock(original: () => Promise<typeof import("@axiom/app")>) {
  const actual = await original();
  return {
    ...actual,
    can: () => true,
    useSession: () => ({ user: { id: "user_01TEST" }, account: "acc_01TEST" }),
    useSessionBound: () => undefined,
  };
}

export async function routerMock(original: () => Promise<typeof import("@tanstack/react-router")>) {
  const actual = await original();
  return {
    ...actual,
    useLocation: () => locationState,
    useNavigate: () => navigateMock,
    Link: ({ to, children }: { to?: string; children?: ReactNode }) => (
      <a href={typeof to === "string" ? to : "#"}>{children}</a>
    ),
  };
}

setUpPages();

beforeEach(() => {
  sessionStorage.clear();
  getMock.mockReset();
  postMock.mockReset();
  navigateMock.mockReset();
  locationState.pathname = "/balance";
  locationState.hash = "";
  mediaState.desktop = true;
  mediaState.listeners.clear();
  window.matchMedia = ((query: string) => {
    const matches = query.includes("min-width: 820px") ? mediaState.desktop : false;
    return {
      matches,
      media: query,
      addEventListener: (_: string, listener: () => void) => mediaState.listeners.add(listener),
      removeEventListener: (_: string, listener: () => void) => mediaState.listeners.delete(listener),
    };
  }) as unknown as typeof window.matchMedia;
  HTMLDialogElement.prototype.close = function () {
    this.open = false;
    this.dispatchEvent(new Event("close"));
  };
});

export function toggleDesktop(desktop: boolean) {
  mediaState.desktop = desktop;
  for (const listener of mediaState.listeners) listener();
}

export function buttonWithText(container: Element, label: string) {
  const found = Array.from(container.querySelectorAll("button")).find((btn) => btn.textContent?.includes(label));
  if (!found) throw new Error(`Missing button containing ${label}`);
  return found;
}

export function setFieldValue(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(proto, "value")?.set?.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

export const usd = (amount: string) => ({ amount, currency: "USD" });
export const cny = (amount: string) => ({ amount, currency: "CNY" });
export const quote = (method: string, total: string, fee = "0") => ({ method, total: cny(total), fee: cny(fee) });
export const balance = (amount: string, reserved = "0") => ({
  balance: usd(amount),
  available: usd((BigInt(amount) - BigInt(reserved)).toString()),
  reserved: usd(reserved),
  floor: usd("0"),
  spent_this_month: usd("0"),
  batches: [],
});

export function makeTestOrder(id: string, creditsAmount: string, totalCny: string) {
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
export function captureTimeoutThenSuccess(keys: string[], responsePayload: unknown) {
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

export function invalidStatementDay(pointer: string) {
  return Promise.resolve({
    error: {
      error: {
        code: "invalid_request",
        message: "the request breaks the contract",
        fields: [{ pointer, message: "invalid" }],
      },
    },
    response: new Response(null, { status: 400 }),
  });
}
