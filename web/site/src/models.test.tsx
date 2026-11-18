// @vitest-environment jsdom
import i18next from "i18next";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { startI18n } from "../../packages/ui/src/i18n";
import en from "./locales/en";
import zh from "./locales/zh";
import { Models } from "./models";

const request = vi.hoisted(() => vi.fn());
const catalogue = vi.fn();
vi.mock("@axiom/api", async (load) => ({
  ...(await load<typeof import("@axiom/api")>()),
  consoleApi: { GET: request },
}));

const price = {
  currency: "USD",
  rates: [
    {
      item: "input",
      service_tier: "standard",
      unit: "token",
      amount: "0.000002",
      headline: true,
      display_amount: "2",
      display_per: 1_000_000,
    },
  ],
};
const model = {
  id: "acme/sonnet",
  name: "Sonnet",
  author: "acme",
  abilities: ["tools"],
  pricing: price,
  endpoints: [{ name: "group-a", pricing: price }],
};

function ok(data: unknown) {
  return { data, response: new Response() };
}

let host: HTMLElement;
let root: Root;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal(
    "matchMedia",
    vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })),
  );
  window.history.replaceState(null, "", "/models");
  localStorage.setItem("axiom.language", "en");
  startI18n("site", { en, zh });
  catalogue.mockResolvedValue(ok({ data: [model] }));
  request.mockImplementation(async (path: string) => (path.endsWith("/models") ? catalogue() : ok({})));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  request.mockReset();
  catalogue.mockReset();
  vi.unstubAllGlobals();
});

async function mount() {
  await act(async () =>
    root.render(
      <I18nextProvider i18n={i18next}>
        <Models />
      </I18nextProvider>,
    ),
  );
}

const latestFilters = () => request.mock.calls.filter(([path]) => path.endsWith("/models")).at(-1)?.[1].params.query;
const button = (name: string) => [...host.querySelectorAll("button")].find((el) => el.textContent === name);

it("applies filters to the public API and restores them from browser history", async () => {
  window.history.replaceState(null, "", "/models?q=son&author=acme&ability=tools");
  await mount();
  expect(latestFilters()).toEqual({ q: "son", author: "acme", ability: "tools" });
  const q = host.querySelector<HTMLInputElement>('input[name="q"]');
  expect(q?.value).toBe("son");
  if (q) q.value = "  nova  ";
  await act(async () => button("Apply")?.click());
  expect(window.location.search).toBe("?q=nova&author=acme&ability=tools");
  expect(latestFilters()).toEqual({ q: "nova", author: "acme", ability: "tools" });
  await act(async () => {
    const popped = new Promise((resolve) => window.addEventListener("popstate", resolve, { once: true }));
    window.history.back();
    await popped;
  });
  expect(latestFilters()).toEqual({ q: "son", author: "acme", ability: "tools" });
  expect(host.querySelector<HTMLInputElement>('input[name="q"]')?.value).toBe("son");
});

it.each([true, false])("opens the model and preserves site price visibility (public=%s)", async (publicPrices) => {
  if (!publicPrices) {
    catalogue.mockResolvedValueOnce(ok({ data: [{ ...model, pricing: undefined, endpoints: [{ name: "group-a" }] }] }));
  }
  await mount();
  expect(host.textContent?.includes("$2.00")).toBe(publicPrices);
  expect(host.textContent?.includes("Prices are visible to signed-in members")).toBe(!publicPrices);
  await act(async () => button("Details")?.click());
  expect(host.querySelector('table[aria-label="Endpoints"]')?.textContent).toContain("group-a");
  expect(Boolean(host.querySelector('table[aria-label^="Sonnet Model price"]'))).toBe(publicPrices);
});

it("retries a failed catalogue request without discarding the current URL", async () => {
  catalogue.mockResolvedValueOnce({
    response: new Response(null, { status: 500, headers: { "X-Request-ID": "request-test" } }),
  });
  window.history.replaceState(null, "", "/models?q=son");
  await mount();
  expect(host.querySelector('[role="alert"]')).not.toBeNull();
  expect(host.textContent).toContain("request-test");
  await act(async () => button("Try again")?.click());
  expect(window.location.search).toBe("?q=son");
  expect(host.querySelector('table[aria-label="Models & prices"]')?.textContent).toContain("Sonnet");
});
