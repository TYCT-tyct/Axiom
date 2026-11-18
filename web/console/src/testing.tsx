import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createInstance } from "i18next";
import { act, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeAll, vi } from "vitest";

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

// answer, as a console call resolves: the data of a 200, or the error code of any other status. A call a test does not
// name is an unavailable node, so a page that reaches outside its own requests fails that test rather than passing.
type Call = (path: string, options?: never) => Promise<unknown>;
type Method = "GET" | "POST" | "PUT" | "DELETE";

export async function mockClient(importOriginal: <T>() => Promise<T>, handlers: Partial<Record<Method, Call>> = {}) {
  const actual = await importOriginal<typeof import("@axiom/api")>();
  const unavailable: Call = () => answer(503, "unavailable");
  return {
    ...actual,
    consoleApi: {
      ...actual.consoleApi,
      GET: vi.fn((handlers.GET ?? unavailable) as never),
      POST: vi.fn((handlers.POST ?? unavailable) as never),
      PUT: vi.fn((handlers.PUT ?? unavailable) as never),
      DELETE: vi.fn((handlers.DELETE ?? unavailable) as never),
    },
  };
}

// wideWindow is jsdom's missing matchMedia for the pages that pick the board's own form: they take the wide board, which
// is what a desktop shows.
export function wideWindow() {
  beforeAll(() => {
    window.matchMedia = ((query: string) => ({
      matches: true,
      media: query,
      addEventListener() {},
      removeEventListener() {},
    })) as unknown as typeof window.matchMedia;
  });
}

// press clicks the element a user reaches by that role and accessible name (or by its text), the way a mouse does.
export async function press(root: ParentNode, name: RegExp | string) {
  const elements = [...root.querySelectorAll<HTMLElement>("[role=button], button")];
  const element = elements.find((candidate) =>
    name instanceof RegExp
      ? name.test(candidate.getAttribute("aria-label") ?? candidate.textContent ?? "")
      : (candidate.getAttribute("aria-label") ?? candidate.textContent) === name,
  );
  if (!element) throw new Error(`no element ${String(name)}`);
  await act(async () => element.click());
  await settle();
}

// setUpBoardPages readies a test of a page ported from the board: the document as setUpPages leaves it, and the wide
// board's breakpoint, which jsdom lacks.
export function setUpBoardPages() {
  setUpPages();
  wideWindow();
}

// openRow opens the row of a board-drawn list that a page supplies an accessible name for, as a reader does with the
// keyboard or the mouse, and returns the dialog it opened.
export async function openRow(container: HTMLElement, label: string, index = 0) {
  const rows = [...container.querySelectorAll<HTMLElement>("[aria-label]")].filter((element) =>
    (element.getAttribute("aria-label") ?? "").startsWith(label),
  );
  await act(async () => rows[index]?.click());
  await settle();
  return document.querySelector("dialog") as HTMLElement;
}
