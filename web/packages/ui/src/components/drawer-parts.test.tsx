// @vitest-environment jsdom
import i18next from "i18next";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { initReactI18next } from "react-i18next";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { DrawerFacts, DrawerSection, QuotaMeter, Tally } from "./drawer-parts";
import { TableToolbar } from "./table-toolbar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// The parts under test only format numbers, so a language and no words are all they need.
beforeAll(() => i18next.use(initReactI18next).init({ lng: "zh", defaultNS: "common", resources: {} }));

let mounted: { root: Root; host: HTMLElement } | undefined;
const mount = () => {
  const host = document.body.appendChild(document.createElement("div"));
  mounted = { root: createRoot(host), host };
  return mounted;
};
beforeEach(mount);
afterEach(() => {
  act(() => mounted?.root.unmount());
  mounted?.host.remove();
});

// The page the test draws into: one for each test.
const host = () => mounted?.host ?? document.body;
const render = (node: ReactNode) => act(() => mounted?.root.render(node));
const key = (target: Element | null, name: string) =>
  act(() => {
    target?.dispatchEvent(new KeyboardEvent("keydown", { key: name, bubbles: true, cancelable: true }));
  });

const toolbar = (search: (value: string) => void, value = "") =>
  render(
    <TableToolbar
      label="users"
      search={{ label: "Find", value, onChange: search }}
      filter={{
        label: "state",
        value: "all",
        options: [
          { value: "all", label: "All" },
          { value: "on", label: "On" },
        ],
        onChange: () => {},
      }}
      shown={3}
      total={10}
      unit="rows"
    />,
  );

it("says how many rows it lists, clears its search on Escape and takes focus on /", () => {
  const search = vi.fn();
  toolbar(search, "ab");
  expect(host().querySelector('[role="status"]')?.textContent).toBe("3 / 10 rows");
  const input = host().querySelector("input[type=search]");
  key(input, "Escape");
  expect(search).toHaveBeenLastCalledWith("");

  key(document.body, "/");
  expect(document.activeElement).toBe(input);
  const other = document.body.appendChild(document.createElement("input"));
  other.focus();
  key(other, "/");
  expect(document.activeElement).toBe(other);
  other.remove();
});

it("clamps the quota bar and marks an allowance used up, and names the line that is the answer", () => {
  render(
    <>
      <QuotaMeter label="today" used="¥12" of="¥10" share={1.2} over note="used up" />
      <Tally
        label="refund"
        rows={[
          { label: "paid", value: "¥10" },
          { label: "at most", value: "¥4", sum: true },
        ]}
      />
    </>,
  );
  const bar = host().querySelector('[role="progressbar"]');
  expect(bar?.getAttribute("aria-valuenow")).toBe("100");
  expect(host().textContent).toContain("used up");
  expect([...host().querySelectorAll("dl[aria-label=refund] dt")].map((dt) => dt.textContent)).toEqual([
    "paid",
    "at most",
  ]);
});

it("names a section by its title and keeps what sits at its end", () => {
  const html = renderToStaticMarkup(
    <DrawerSection title="Money" aside={<b>{"tag"}</b>}>
      <p>{"body"}</p>
    </DrawerSection>,
  );
  expect(html).toMatch(/<h3[^>]*>Money<\/h3><b>tag<\/b>/);
  expect(html).toContain("<p>body</p>");
});

it("lists each fact as a term and its value, with no box of its own", () => {
  const html = renderToStaticMarkup(
    <DrawerFacts
      rows={[
        ["Charged", "¥1.20"],
        ["Paid", <i key="paid">{"¥0.80"}</i>],
      ]}
    />,
  );
  expect(html.match(/<dt>/g)).toHaveLength(2);
  expect(html).toContain("<dd><i>¥0.80</i></dd>");
  expect(html).not.toContain("<section");
});
