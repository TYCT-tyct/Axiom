// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createInstance } from "i18next";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { I18nextProvider } from "react-i18next";
import { beforeAll, expect, it, vi } from "vitest";
import en from "../locales/en";
import { Classes } from "./source-classes";

vi.mock("@axiom/app", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@axiom/app")>();
  return {
    ...actual,
    useErrors: () => ({ message: () => "failed", field: () => undefined }),
    useSession: () => ({ user: { id: "u1" }, account: "a1" }),
  };
});

const i18n = createInstance();

// React 19 asks for this before act() runs outside a test renderer.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

beforeAll(async () => {
  // jsdom has the dialog element but not its modal methods.
  HTMLDialogElement.prototype.showModal = function () {
    this.open = true;
  };
  HTMLDialogElement.prototype.close = function () {
    this.open = false;
  };
  await i18n.init({ lng: "en", defaultNS: "admin", resources: { en: { admin: en } } });
});

// A class may be called "new": its edit button opens its editor, not the one that creates a class.
it("edits a class whose slug is new", async () => {
  const cls = {
    slug: "new",
    name: "New supply",
    name_zh: "",
    description: "",
    position: 500,
    fallback: false,
    system: false,
    revision: 3,
    updated_at: "2026-11-03T00:00:00Z",
    rules: { match: "all" as const, conditions: [] },
  };
  const container = document.createElement("div");
  document.body.append(container);
  const root = createRoot(container);
  await act(async () => {
    root.render(
      <QueryClientProvider client={new QueryClient()}>
        <I18nextProvider i18n={i18n}>
          <Classes classes={[cls]} />
        </I18nextProvider>
      </QueryClientProvider>,
    );
  });
  const edit = [...container.querySelectorAll("li button")].find((b) => b.textContent === en.sourceClasses.edit);
  await act(async () => {
    (edit as HTMLButtonElement).click();
  });
  expect(document.body.textContent).toContain(i18n.t("sourceClasses.editTitle", { name: "New supply" }));
  expect(document.querySelector('input[name="slug"]')).toBeNull();
  await act(async () => root.unmount());
  container.remove();
});
