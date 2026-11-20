// @vitest-environment jsdom
import { ApiError, type Schemas } from "@axiom/api";
import { useQuery } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { act } from "react";
import { expect, it, vi } from "vitest";
import { render, settle, setUpPages } from "../testing";
import { InlineCallSplit } from "./calls-detail";
import { CallsFilterBar } from "./calls-filters";

const t = ((key: string) => key) as TFunction;

setUpPages();
// the filter bar asks whether the page is a phone's width, which lays the time ranges out in two rows
vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));

it("keeps the call list, close and retry controls in a failed detail drawer without a second overview", async () => {
  const getCall = vi.fn<() => Promise<Schemas["Call"]>>().mockRejectedValue(new ApiError(500, null));
  const onClose = vi.fn();
  function Drawer() {
    const query = useQuery({ queryKey: ["detail"], queryFn: getCall });
    return (
      <InlineCallSplit
        phone={false}
        headers={[]}
        rows={[{ key: "day", kind: "0", f0: "lgday", f1: "Oct 14", f2: "", f3: "1 loaded call" }]}
        moreLabel=""
        hideMore
        moreDisabled={false}
        onMore={() => {}}
        onListClick={() => {}}
        selectedQuery={query}
        keyNames={new Map()}
        hasPrev={false}
        hasNext={false}
        onPrev={() => {}}
        onNext={() => {}}
        onClose={onClose}
        t={t}
      />
    );
  }

  const { container } = await render(<Drawer />);
  const alert = container.querySelector('[role="alert"]');
  expect(alert?.textContent).toContain("callsBoard.states.retry");
  expect(container.querySelector('[aria-label="callsBoard.list.tableLabel"]')?.textContent).toContain("1 loaded call");
  expect(container.querySelector("h1, .usf, .lgsum")).toBeNull();
  await act(() => alert?.querySelector("button")?.click());
  expect(getCall).toHaveBeenCalledTimes(2);
  await act(() => container.querySelector<HTMLElement>('[aria-label="callsBoard.detail.close"]')?.click());
  expect(onClose).toHaveBeenCalledOnce();
  await settle();
});

it("discards the custom date draft on reset and when navigating away from its filters", async ({ onTestFinished }) => {
  const scroll = vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  onTestFinished(() => {
    scroll.mockRestore();
  });
  const root = createRootRoute();
  const route = createRoute({
    getParentRoute: () => root,
    path: "/calls",
    validateSearch: (search: Record<string, unknown>) => ({
      since: typeof search.since === "string" ? search.since : undefined,
      preset: search.preset === "d7" ? ("d7" as const) : undefined,
    }),
    component: Filters,
  });
  function Filters() {
    const { since, preset } = route.useSearch();
    const navigate = route.useNavigate();
    return (
      <CallsFilterBar
        t={t}
        filters={{ since }}
        preset={preset}
        canKeys={false}
        hasMoreKeys={false}
        onLoadMoreKeys={() => {}}
        models={[]}
        keys={[]}
        workspaces={[]}
        onApply={(search) => void navigate({ search })}
      />
    );
  }
  const history = createMemoryHistory({ initialEntries: ["/calls"] });
  const router = createRouter({ history, routeTree: root.addChildren([route]) });
  const { container } = await render(<RouterProvider router={router} />);
  const range = (choice: string) =>
    Array.from(container.querySelectorAll("button")).find(
      (button) => button.textContent === `callsBoard.range.${choice}`,
    );
  const clear = () => act(() => container.querySelector("a")?.click());
  const preset = range("d7");
  await act(() => {
    preset?.focus();
    preset?.click();
  });
  expect(history.location.search).toContain("preset=d7");
  expect(document.activeElement).toBe(preset);
  await act(() => range("custom")?.click());
  expect(range("custom")?.classList.contains("on")).toBe(true);
  await clear();
  expect(history.location.search).toBe("");
  expect(range("all")?.classList.contains("on")).toBe(true);
  await act(() => range("custom")?.click());
  await clear();
  expect(range("all")?.classList.contains("on")).toBe(true);
  await act(() => range("d7")?.click());
  await act(() => range("custom")?.click());
  await act(() => history.back());
  expect(range("all")?.classList.contains("on")).toBe(true);
  await act(() => history.forward());
  expect(range("d7")?.classList.contains("on")).toBe(true);
});
