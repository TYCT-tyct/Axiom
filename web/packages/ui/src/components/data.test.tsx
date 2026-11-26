// @vitest-environment jsdom
import { createInstance } from "i18next";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import en from "../locales/en";
import zh from "../locales/zh";
import { BarChart, LineChart } from "./chart";
import { DataTable } from "./data-table";
import { Delta } from "./delta";
import { type Filter, FilterBar, filterDefaults } from "./filter-bar";
import { ListDetail } from "./list-detail";
import { Metric } from "./metric";
import { PaceBar } from "./pace-bar";
import { RingChart } from "./ring-chart";
import { Sparkline } from "./sparkline";
import { Tooltip } from "./tooltip";

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

it("draws a single line point as a dot", () => {
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

// --- DataTable ------------------------------------------------------------------------------------------------------

type Use = { id: string; name: string; used: number | null };
const uses: Use[] = [
  { id: "a", name: "alpha", used: 2 },
  { id: "b", name: "beta", used: 9 },
  { id: "c", name: "gamma", used: null },
  { id: "d", name: "delta", used: 5 },
];
const usesTable = (extra: { rows?: Use[]; total?: number; flash?: string } = {}) =>
  render(
    <DataTable<Use>
      label="uses"
      rows={extra.rows ?? uses}
      rowKey={(row) => row.id}
      columns={[
        { key: "name", header: "name", cell: (row) => row.name },
        { key: "used", header: "used", align: "end", cell: (row) => row.used ?? "-", sort: (row) => row.used },
      ]}
      total={extra.total}
      unit="rows"
      flash={extra.flash}
    />,
  );
const order = () => [...host.querySelectorAll("tbody tr")].map((tr) => tr.firstElementChild?.textContent);
const header = (n: number) => host.querySelectorAll("th")[n];

it("steps a numeric column through high to low, low to high and off, saying the order on its header", () => {
  usesTable();
  expect(header(0)?.hasAttribute("aria-sort")).toBe(false);
  expect(header(0)?.querySelector("button")).toBeNull();
  const steps = [
    { sort: "descending", rows: ["beta", "delta", "alpha", "gamma"] },
    { sort: "ascending", rows: ["gamma", "alpha", "delta", "beta"] },
    { sort: "none", rows: ["alpha", "beta", "gamma", "delta"] },
  ];
  expect(header(1)?.getAttribute("aria-sort")).toBe("none");
  for (const step of steps) {
    act(() => header(1)?.querySelector("button")?.click());
    expect(header(1)?.getAttribute("aria-sort")).toBe(step.sort);
    expect(order()).toEqual(step.rows);
  }
});

it.each([
  { case: "every row", rows: uses, total: 4, count: "4 rows" },
  { case: "a filtered few", rows: uses.slice(0, 3), total: 10, count: "3 / 10 rows" },
])("says how many rows it lists: $case", ({ rows, total, count }) => {
  usesTable({ rows, total });
  expect(text('[role="status"]')).toBe(count);
});

it("lights a row an edit changed once, and lets the light go when it has played", () => {
  usesTable({ flash: "b" });
  const lit = () => [...host.querySelectorAll("tbody tr")].map((tr) => tr.className.includes("flash"));
  expect(lit()).toEqual([false, true, false, false]);
  // jsdom has no AnimationEvent, so React listens for the prefixed name a browser without it would send.
  act(() => {
    host
      .querySelectorAll("tbody tr")[1]
      ?.firstElementChild?.dispatchEvent(new Event("webkitAnimationEnd", { bubbles: true }));
  });
  expect(lit()).toEqual([false, false, false, false]);
});

// --- RingChart ------------------------------------------------------------------------------------------------------

const point = (target: Element | null | undefined, type: "pointerover" | "pointerout") =>
  act(() => {
    target?.dispatchEvent(new MouseEvent(type, { bubbles: true, relatedTarget: document.body }));
  });

it("links the ring and its list both ways, and says every part in its label", () => {
  const picked = vi.fn();
  render(
    <RingChart
      label="mix"
      centre="61%"
      centreLabel="top"
      onSelect={picked}
      parts={[
        { key: "a", label: "alpha", value: 50, share: "50%", colour: "data" },
        { key: "b", label: "beta", value: 30, share: "30%", colour: "data3" },
        { key: "c", label: "gamma", value: 20, share: "20%", colour: "data2" },
      ]}
    />,
  );
  expect(host.querySelector('[role="img"]')?.getAttribute("aria-label")).toBe("mix: alpha 50%, beta 30%, gamma 20%");
  const arcs = () => [...host.querySelectorAll("circle")].map((c) => (c.getAttribute("class") ?? "").includes("hot"));
  const rows = () => [...host.querySelectorAll("li > *")].map((r) => r.className.includes("lit"));
  point(host.querySelectorAll("li")[1], "pointerover");
  expect(arcs()).toEqual([false, true, false]);
  point(host.querySelectorAll("li")[1], "pointerout");
  expect(arcs()).toEqual([false, false, false]);
  point(host.querySelectorAll("circle")[2], "pointerover");
  expect(rows()).toEqual([false, false, true]);
  point(host.querySelectorAll("circle")[2], "pointerout");
  act(() => host.querySelectorAll("button")[0]?.focus());
  expect(arcs()).toEqual([true, false, false]);
  act(() => host.querySelectorAll("button")[0]?.click());
  expect(picked).toHaveBeenCalledWith("a");
});

// --- Tooltip --------------------------------------------------------------------------------------------------------

const tip = () => host.querySelector('[role="tooltip"]');

it("shows a truncated cell's full text when it takes focus, and hides it on Esc", () => {
  const full = "a model name far too long for its column";
  render(<Tooltip text={full} />);
  const cell = host.querySelector<HTMLElement>("[tabindex]");
  expect(cell?.textContent).toBe(full);
  expect(tip()).toBeNull();
  act(() => cell?.focus());
  expect(tip()?.textContent).toBe(full);
  press(cell, "Escape");
  expect(tip()).toBeNull();
});

it("describes what it wraps, after a pointer rests on it", () => {
  vi.useFakeTimers();
  render(
    <Tooltip text="spread over the days">
      <b>{"mark"}</b>
    </Tooltip>,
  );
  const trigger = host.querySelector<HTMLElement>("[tabindex]");
  point(trigger, "pointerover");
  expect(tip()).toBeNull();
  act(() => vi.advanceTimersByTime(400));
  expect(trigger?.getAttribute("aria-describedby")).toBe(tip()?.id);
  point(trigger, "pointerout");
  expect(tip()).toBeNull();
  vi.useRealTimers();
});

// --- small displays -------------------------------------------------------------------------------------------------

it.each([
  { used: 4, max: 5, warn: false },
  { used: 5, max: 5, warn: true },
  { used: 7, max: 5, warn: true },
])("turns a pace bar to the warning colour at its limit: $used of $max", ({ used, max, warn }) => {
  render(<PaceBar label="concurrency" value={used} max={max} />);
  const bar = host.querySelector('[role="progressbar"]');
  expect(bar?.getAttribute("aria-valuenow")).toBe(String(Math.min(used, max)));
  expect(bar?.firstElementChild?.className.includes("warn")).toBe(warn);
});

it.each([
  { value: 3.9, tone: "good" as const, reads: "+3.9%" },
  { value: -0.4, tone: "bad" as const, reads: "−0.4%" },
  { value: 2, tone: undefined, reads: "+2%" },
])("writes the sign of a move $value in words as well as the arrow", ({ value, tone, reads }) => {
  render(<Delta value={value} tone={tone} />);
  expect(`${host.querySelector("svg")?.getAttribute("aria-label")}${host.textContent}`).toBe(reads);
  expect(host.querySelector("em")?.className.includes(tone ?? "none")).toBe(tone !== undefined);
});

it("ends a sparkline in a dot at the latest value, and draws a flat rule for none", () => {
  render(<Sparkline values={[1, 3, 2]} />);
  const dot = host.querySelector("circle");
  expect([dot?.getAttribute("cx"), dot?.getAttribute("cy")]).toEqual(["92.0", "12.0"]);
  render(<Sparkline values={[]} label="calls" />);
  expect(host.querySelector("circle")).toBeNull();
  expect(host.querySelector('[role="img"]')?.getAttribute("aria-label")).toBe("calls");
});

it("names the current slot and the baseline on the chart's axes", () => {
  const points = Array.from({ length: 10 }, (_, n) => ({ at: at(n + 1), values: { a: n } }));
  render(
    <BarChart
      label="u"
      empty="-"
      series={series}
      points={points}
      unit="count"
      step="day"
      timeZone="UTC"
      current={9}
      zero
    />,
  );
  const yAxis = [...host.querySelectorAll("figure > div > div:first-child span")].map((span) => span.textContent);
  expect(yAxis[0]).toBe("0");
  const xAxis = [...host.querySelectorAll("figure > div[aria-hidden] span")];
  expect(xAxis.at(-1)?.textContent).toBe("11月10日");
  expect(xAxis.at(-1)?.className.includes("now")).toBe(true);
});
