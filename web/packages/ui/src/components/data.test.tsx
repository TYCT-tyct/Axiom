// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { createInstance } from "i18next";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import en from "../locales/en";
import zh from "../locales/zh";
import { BarChart, LineChart } from "./chart";
import { type Filter, FilterBar, filterDefaults } from "./filter-bar";
import { ListDetail } from "./list-detail";
import { Metric } from "./metric";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const i18n = createInstance();
let root: Root;
let host: HTMLElement;
let narrow = false;
const scrollBy = vi.fn();

beforeEach(async () => {
  await i18n.init({ lng: "zh", defaultNS: "common", resources: { zh: { common: zh }, en: { common: en } } });
  narrow = false;
  window.matchMedia = ((query: string) => ({
    matches: narrow && query.includes("1023"),
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia;
  window.scrollBy = scrollBy;
  // jsdom has no modal dialogs: this one opens, and closing it fires the close event as a browser does.
  HTMLDialogElement.prototype.showModal = function showModal() {
    this.setAttribute("open", "");
  };
  HTMLDialogElement.prototype.close = function close() {
    this.removeAttribute("open");
    this.dispatchEvent(new Event("close"));
  };
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  scrollBy.mockClear();
});

const render = (node: ReactNode) => act(() => root.render(<I18nextProvider i18n={i18n}>{node}</I18nextProvider>));
const press = (target: Element | null, key: string) =>
  act(() => {
    target?.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
  });
const text = (selector: string) => host.querySelector(selector)?.textContent;

// --- ListDetail -----------------------------------------------------------------------------------------------------

const rows = Array.from({ length: 10_000 }, (_, n) => ({ id: `r${n}`, name: `row ${n}` }));

function List({ selected, onSelect }: { selected: string | null; onSelect: (key: string | null, by: string) => void }) {
  return (
    <ListDetail
      label="calls"
      detailLabel="call detail"
      placeholder="pick one"
      rows={rows}
      rowKey={(row) => row.id}
      renderRow={(row) => row.name}
      selected={selected}
      onSelect={onSelect}
      detail={(row) => <p>{`detail of ${row.name}`}</p>}
    />
  );
}

const options = () => [...host.querySelectorAll<HTMLElement>('[role="option"]')];

it("draws only the rows near the viewport of a 10,000-row list, and says how many there are", () => {
  render(<List selected={null} onSelect={() => {}} />);
  expect(options().length).toBeGreaterThan(10);
  expect(options().length).toBeLessThan(100);
  expect(options()[0]?.getAttribute("aria-setsize")).toBe("10000");
  expect(text("section[aria-label]")).toBe("pick one");
});

it("moves the selection with the arrow keys beside the list, and keeps the selected row drawn", () => {
  const onSelect = vi.fn();
  render(<List selected="r2" onSelect={onSelect} />);
  const row = options().find((option) => option.dataset.at === "2");
  expect(row?.tabIndex).toBe(0);
  expect(text("section[aria-label]")).toBe("detail of row 2");
  press(row ?? null, "ArrowDown");
  expect(onSelect).toHaveBeenLastCalledWith("r3", "key");
  press(row ?? null, "End");
  expect(onSelect).toHaveBeenLastCalledWith("r9999", "key");
  press(row ?? null, "Home");
  expect(onSelect).toHaveBeenLastCalledWith("r0", "key");
  press(options()[0] ?? null, "ArrowUp");
  expect(onSelect).toHaveBeenLastCalledWith("r0", "key");
});

it("covers the list with a panel on a narrow window, and returns focus to its row when it closes", () => {
  narrow = true;
  const onSelect = vi.fn();
  render(<List selected={null} onSelect={onSelect} />);
  expect(host.querySelector("dialog")).toBeNull();
  press(options()[4] ?? null, "ArrowDown");
  expect(onSelect).not.toHaveBeenCalled();
  press(options()[4] ?? null, "Enter");
  expect(onSelect).toHaveBeenLastCalledWith("r4", "pointer");

  render(<List selected="r4" onSelect={onSelect} />);
  const panel = host.querySelector("dialog");
  expect(panel?.hasAttribute("open")).toBe(true);
  expect(panel?.getAttribute("aria-label")).toBe("call detail");
  expect(panel?.textContent).toContain("detail of row 4");

  act(() => panel?.close());
  expect(onSelect).toHaveBeenLastCalledWith(null, "close");
  render(<List selected={null} onSelect={onSelect} />);
  expect(host.querySelector("dialog")).toBeNull();
  expect((document.activeElement as HTMLElement).dataset.at).toBe("4");
});

it("opens the panel when a window with a selected row narrows past the breakpoint", () => {
  render(<List selected="r4" onSelect={() => {}} />);
  expect(host.querySelector("dialog")).toBeNull();
  narrow = true;
  render(<List selected="r4" onSelect={() => {}} />);
  expect(host.querySelector("dialog")?.hasAttribute("open")).toBe(true);
  narrow = false;
  render(<List selected="r4" onSelect={() => {}} />);
  expect(host.querySelector("dialog")).toBeNull();
  expect(text("section[aria-label]")).toContain("detail of row 4");
});

it("draws a list that shrinks, or empties, under a window measured for the long one", () => {
  const show = (list: typeof rows) =>
    render(
      <ListDetail
        label="calls"
        detailLabel="d"
        placeholder="p"
        rows={list}
        rowKey={(row) => row.id}
        renderRow={(row) => row.name}
        selected={null}
        onSelect={() => {}}
        detail={() => null}
      />,
    );
  show(rows);
  show(rows.slice(0, 1));
  expect(options().map((option) => option.textContent)).toEqual(["row 0"]);
  show([]);
  expect(options()).toEqual([]);
});

// --- FilterBar ------------------------------------------------------------------------------------------------------

const filters: Filter<"q" | "status">[] = [
  { kind: "search", key: "q", label: "Search", initial: "" },
  {
    kind: "segmented",
    key: "status",
    label: "Status",
    initial: "all",
    options: [
      { value: "all", label: "All" },
      { value: "ok", label: "OK" },
    ],
  },
];

it("shows Clear only while something is filtered, and reports each change", () => {
  const onChange = vi.fn();
  const onClear = vi.fn();
  const bar = (values: { q: string; status: string }) =>
    render(<FilterBar label="Filters" filters={filters} values={values} onChange={onChange} onClear={onClear} />);
  expect(filterDefaults(filters)).toEqual({ q: "", status: "all" });
  bar({ q: "", status: "all" });
  expect(host.querySelector("button")).toBeNull();
  bar({ q: "", status: "ok" });
  const clear = host.querySelector("button");
  expect(clear?.textContent).toBe("清除筛选");
  act(() => clear?.click());
  expect(onClear).toHaveBeenCalledOnce();
  act(() => host.querySelector<HTMLInputElement>('input[type="radio"][value="all"]')?.click());
  expect(onChange).toHaveBeenCalledWith("status", "all");
});

type Point = { at: Date; values: Record<string, number> };

// --- Chart ----------------------------------------------------------------------------------------------------------

const series = [
  { key: "a", label: "alpha" },
  { key: "b", label: "beta" },
];
// 20:00 UTC: the next day in Shanghai, so a time zone moves the date.
const at = (day: number) => new Date(Date.UTC(2026, 10, day, 20));
const chart = (points: Point[], extra: { timeZone?: string; unit?: "count" | { currency: string } } = {}) =>
  render(
    <BarChart
      label="usage"
      empty="no data"
      series={series}
      points={points}
      unit={extra.unit ?? "count"}
      step="day"
      timeZone={extra.timeZone ?? "Asia/Shanghai"}
    />,
  );
const cells = () => [...host.querySelectorAll("tbody tr")].map((tr) => [...tr.children].map((c) => c.textContent));

it.each<{ case: string; points: Point[]; bars: number; table: string[][]; axis: string[] }>([
  { case: "no points", points: [], bars: 0, table: [], axis: ["1", "2", "3"] },
  {
    case: "one point",
    points: [{ at: at(9), values: { a: 2 } }],
    bars: 1,
    table: [["2026年11月10日", "2", "0"]],
    axis: ["1", "2", "3"],
  },
  {
    case: "zeros and a huge value",
    points: [
      { at: at(9), values: { a: 0, b: 0 } },
      { at: at(10), values: { a: 1e15, b: Number.POSITIVE_INFINITY } },
      { at: at(11), values: { a: -5 } },
    ],
    bars: 1,
    table: [
      ["2026年11月10日", "0", "0"],
      ["2026年11月11日", "1,000,000,000,000,000", "0"],
      ["2026年11月12日", "0", "0"],
    ],
    axis: ["500,000,000,000,000", "1,000,000,000,000,000", "1,500,000,000,000,000"],
  },
])(
  "draws $case without error, with a sensible axis and the same numbers in a table",
  ({ points, bars, table, axis }) => {
    chart(points);
    expect(host.querySelectorAll("rect[height]:not([class*=hit])").length).toBe(bars);
    expect(cells()).toEqual(table);
    expect([...host.querySelectorAll("figure > div > div:first-child span")].map((span) => span.textContent)).toEqual(
      axis,
    );
    expect(text("p")).toBe(points.length === 0 ? "no data" : undefined);
    expect(host.innerHTML).not.toContain("NaN");
  },
);

it("reads the chart point by point from the keyboard, announcing each point", () => {
  chart([
    { at: at(9), values: { a: 3, b: 1 } },
    { at: at(10), values: { a: 5, b: 2 } },
  ]);
  const plot = host.querySelector<HTMLElement>('[role="group"]');
  expect(plot?.getAttribute("aria-label")).toBe("usage");
  act(() => plot?.focus());
  expect(text('[role="status"]')).toBe("2026年11月10日: alpha 3, beta 1");
  press(plot, "ArrowRight");
  expect(text('[role="status"]')).toBe("2026年11月11日: alpha 5, beta 2");
  press(plot, "ArrowRight");
  expect(text('[role="status"]')).toBe("2026年11月11日: alpha 5, beta 2");
  press(plot, "Escape");
  expect(text('[role="status"]')).toBe("");
});

it("formats axis, tooltip and metric for the language, time zone and currency in force", async () => {
  const points = [{ at: at(9), values: { a: 1500 } }];
  const show = (zone: string) => {
    render(
      <>
        <LineChart
          label="spend"
          empty="-"
          series={series}
          points={points}
          unit={{ currency: "USD" }}
          step="day"
          timeZone={zone}
        />
        <Metric label="spend" value={{ amount: "1500000000", currency: "USD" }} delta={0.125} timeZone={zone} />
      </>,
    );
    return host.textContent;
  };
  const zhShanghai = show("Asia/Shanghai");
  expect(zhShanghai).toContain("2026年11月10日");
  expect(zhShanghai).toContain("US$1,500.00");
  expect(zhShanghai).toContain("US$1.50");
  expect(zhShanghai).toContain("+12.5%");
  await act(() => i18n.changeLanguage("en"));
  const enNewYork = show("America/New_York");
  expect(enNewYork).toContain("Nov 9, 2026");
  expect(enNewYork).toContain("$1,500.00");
  expect(enNewYork).toContain("$1.50");
  await act(() => i18n.changeLanguage("zh"));
});

it("shows an exact amount as the server gave it, in the tooltip, the announcement and the table", () => {
  const amount = "9223372036854775807";
  render(
    <BarChart
      label="spend"
      empty="-"
      series={series}
      points={[{ at: at(9), values: { a: { amount, currency: "USD" } } }]}
      unit={{ currency: "USD" }}
      step="day"
    />,
  );
  const plot = host.querySelector<HTMLElement>('[role="group"]');
  act(() => plot?.focus());
  const exact = "US$9,223,372,036.854775807";
  expect(cells()[0]?.[1]).toBe(exact);
  expect(text('[role="status"]')).toContain(exact);
  expect(host.textContent).toContain(exact);
});

it("draws a single line point as a dot and plays no entrance animation under reduced motion", () => {
  render(
    <LineChart
      label="u"
      empty="-"
      series={series}
      points={[{ at: at(9), values: { a: 4 } }]}
      unit="count"
      step="day"
    />,
  );
  expect(host.querySelector("path")?.getAttribute("d")).toMatch(/h0$/);
  const css = readFileSync(`${import.meta.dirname}/chart.module.css`, "utf8");
  const reduced = css.slice(css.indexOf("prefers-reduced-motion"));
  expect(reduced).toMatch(/\.bar,[^}]*\.line,[^}]*\.tip\s*{\s*animation: none/);
});

// --- Metric ---------------------------------------------------------------------------------------------------------

it.each([
  { value: 12840, delta: -0.032, want: ["12,840", "-3.2%"] },
  { value: 0, delta: 0, want: ["0", "0%"] },
  { value: 0, delta: undefined, want: ["0"] },
])("formats the metric $value with delta $delta", ({ value, delta, want }) => {
  render(<Metric label="requests" value={value} delta={delta} />);
  for (const part of want) {
    expect(host.textContent).toContain(part);
  }
  expect(host.querySelector("em") === null).toBe(delta === undefined);
});
