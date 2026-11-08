import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createInstance } from "i18next";
import { act, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeAll } from "vitest";

// What the component tests of the console share: a page rendered with its query cache and messages, and the ways a user
// moves it on. Messages have no resources, so a page shows its message keys, which a test can look for.
const i18n = createInstance();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// setUpPages readies the test file's document: messages, the modal methods jsdom's dialog element lacks, and an empty
// body after each test.
export function setUpPages() {
  beforeAll(async () => {
    await i18n.init({ lng: "en", resources: {} });
    HTMLDialogElement.prototype.showModal = function () {
      this.open = true;
    };
  });
  afterEach(() => {
    document.body.replaceChildren();
  });
}

// answer is what the mocked console client resolves to: the data of a 200, or the error code of any other status.
export const answer = (status: number, data: unknown) =>
  Promise.resolve(
    status === 200
      ? { data, response: new Response(null, { status }) }
      : { error: { error: { code: data, message: "" } }, response: new Response(null, { status }) },
  );

// everyPermission is the session module for a test that is not about permissions: the session may do everything.
export async function everyPermission(importOriginal: <T>() => Promise<T>) {
  const actual = await importOriginal<typeof import("@axiom/app")>();
  return { ...actual, can: () => true, useSession: () => ({}) };
}

// settle lets answered reads reach the page: the query cache tells its observers on a later task, after act's own.
export const settle = () => act(() => new Promise((resolve) => setTimeout(resolve, 0)));

export async function render(node: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } });
  const container = document.createElement("div");
  document.body.append(container);
  await act(async () => {
    createRoot(container).render(
      <QueryClientProvider client={client}>
        <I18nextProvider i18n={i18n}>{node}</I18nextProvider>
      </QueryClientProvider>,
    );
  });
  await settle();
  return { client, container };
}

// choose picks an option the way a user does: the browser sets the value, then reports the change.
export async function choose(select: HTMLSelectElement, value: string) {
  await act(async () => {
    select.value = value;
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

export async function submit(form: HTMLFormElement) {
  await act(async () => {
    form.requestSubmit();
  });
  await settle();
}
