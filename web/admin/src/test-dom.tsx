import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createInstance } from "i18next";
import { act, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { I18nextProvider } from "react-i18next";
import en from "./locales/en";

// What a component test in jsdom needs to render admin UI: React's act environment, the admin's English words, and a
// query client. A file of its own so each test does not carry a copy of this setup.

// React 19 asks for this before act() runs outside a test renderer.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// jsdom has the dialog element but not its modal methods, which every dialog of the admin opens and closes with.
HTMLDialogElement.prototype.showModal = function () {
  this.open = true;
};
HTMLDialogElement.prototype.close = function () {
  this.open = false;
};

export const i18n = createInstance();
const ready = i18n.init({ lng: "en", defaultNS: "admin", resources: { en: { admin: en } } });

// renderInDom mounts node in a fresh container and returns it with a way to take it down again.
export async function renderInDom(node: ReactNode) {
  await ready;
  const container = document.createElement("div");
  document.body.append(container);
  const root = createRoot(container);
  await act(async () => {
    root.render(
      <QueryClientProvider client={new QueryClient()}>
        <I18nextProvider i18n={i18n}>{node}</I18nextProvider>
      </QueryClientProvider>,
    );
  });
  return {
    container,
    unmount: async () => {
      await act(async () => root.unmount());
      container.remove();
    },
  };
}

// signedIn is @axiom/app for vi.mock, with an operator signed in who holds permissions, and errors that read "failed":
// vi.mock("@axiom/app", async (original) => (await import("./test-dom")).signedIn(original, ["sites.manage"])).
export async function signedIn(importOriginal: <T>() => Promise<T>, permissions: string[] = []) {
  const actual = await importOriginal<typeof import("@axiom/app")>();
  return {
    ...actual,
    useErrors: () => ({ message: () => "failed", field: () => undefined }),
    useSession: () => ({ user: { id: "u1" }, account: "a1", permissions }),
  };
}

// withAdminApi is @axiom/api for vi.mock, with adminApi answering by the given methods:
// vi.mock("@axiom/api", async (original) => (await import("./test-dom")).withAdminApi(original, { GET: vi.fn(...) })).
export async function withAdminApi(importOriginal: <T>() => Promise<T>, adminApi: Record<string, unknown>) {
  return { ...(await importOriginal<typeof import("@axiom/api")>()), adminApi };
}
