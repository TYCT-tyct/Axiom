// @vitest-environment jsdom
import { act, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { BarRows } from "./bar-rows";
import { MetricStrip } from "./metric-strip";
import { ProposalCard } from "./proposal-card";
import { RingChart } from "./ring-chart";
import { RowList } from "./row-list";
import { StatusDot } from "./status-dot";

// The admin's rows, bars, cards and strips. None of them has words of its own, so no translation is mounted.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// Each render mounts into a fresh container, so a test that renders twice reads the second.
let host: HTMLElement = document.body;
const render = (node: ReactNode) => {
  host = document.body.appendChild(document.createElement("div"));
  act(() => createRoot(host).render(node));
};

afterEach(() => document.body.replaceChildren());

// Words a test sets in JSX, which the lint keeps to names; a link is given empty and takes its content from the row.
const blank = null;
const words = { why: "why", approve: "Approve", aside: "Where points went", dot: "98.2%" };

it("lists rows, makes a row with a link a link with its arrow, and folds a long list under a button", () => {
  const more = vi.fn();
  render(
    <RowList
      label="To do"
      items={[
        { key: "a", tone: "bad", title: "Alert", sub: "why", end: "−12", link: <a href="/alerts">{blank}</a> },
        { key: "b", title: "A fact" },
      ]}
      more={{ label: "See 3 more", onPress: more }}
    />,
  );
  const rows = host.querySelectorAll("li");
  expect(rows).toHaveLength(2);
  expect(rows[0]?.querySelector("a")?.getAttribute("href")).toBe("/alerts");
  expect(rows[0]?.querySelector("a")?.textContent).toContain("Alert");
  expect(rows[0]?.querySelector("a svg")).not.toBeNull();
  expect(rows[1]?.querySelector("a")).toBeNull();
  host.querySelector("button")?.click();
  expect(more).toHaveBeenCalledOnce();
});

it("says so when a list has nothing, and draws nothing when it has no words for it", () => {
  render(<RowList label="To do" items={[]} empty="Nothing to do" />);
  expect(host.textContent).toBe("Nothing to do");
  render(<RowList label="To do" items={[]} />);
  expect(host.textContent).toBe("");
});

it("draws each bar against the largest, with a sliver for a small count, and links a row", () => {
  render(
    <BarRows
      label="Causes"
      rows={[
        { key: "a", label: "timeout", value: 40, end: "40", tone: "bad", link: <a href="/calls">{blank}</a> },
        { key: "b", label: "other", value: 10, end: "10" },
        { key: "c", label: "rare", value: 0, end: "0" },
      ]}
    />,
  );
  const widths = [...host.querySelectorAll("i")].map((bar) => bar.style.getPropertyValue("--w"));
  expect(widths).toEqual(["100.0%", "25.0%", "1.5%"]);
  expect(host.querySelectorAll("a")).toHaveLength(1);
});

it("wraps a strip's cells into rows of the asked number, each with its trend", () => {
  render(
    <MetricStrip
      label="Figures"
      columns={4}
      items={Array.from({ length: 8 }, (_, at) => ({
        key: at,
        label: `L${at}`,
        value: String(at),
        trend: <svg data-trend={at} />,
      }))}
    />,
  );
  expect(host.querySelectorAll("section > div")).toHaveLength(8);
  expect(host.querySelectorAll("[data-trend]")).toHaveLength(8);
  expect(host.querySelector("section")?.className).toMatch(/c4/);
});

it("folds a decided card away and takes it out of the tab order", () => {
  render(
    <ProposalCard
      kind="Gift"
      title="Northwind"
      why={<p>{words.why}</p>}
      actions={<button type="button">{words.approve}</button>}
    />,
  );
  const fold = host.firstElementChild as HTMLElement;
  expect(fold.hasAttribute("inert")).toBe(false);
  expect(host.querySelector("section")?.getAttribute("aria-labelledby")).toBeTruthy();
  render(
    <ProposalCard
      kind="Gift"
      title="Northwind"
      why={<p>{words.why}</p>}
      actions={<button type="button">{words.approve}</button>}
      leaving
    />,
  );
  expect((host.firstElementChild as HTMLElement).hasAttribute("inert")).toBe(true);
});

it("draws a ring beside words of its own in place of the list of parts", () => {
  render(
    <RingChart
      label="Health 92"
      parts={[
        { key: "a", label: "", value: 92, share: "", colour: "ok" },
        { key: "b", label: "", value: 8, share: "", colour: "track" },
      ]}
      centre="92"
      aside={<p>{words.aside}</p>}
    />,
  );
  expect(host.querySelector('[role="img"]')?.getAttribute("aria-label")).toBe("Health 92");
  expect(host.textContent).toContain("Where points went");
  expect(host.querySelectorAll("li")).toHaveLength(0);
});

it("shows a figure with its state as a dot the screen reader skips", () => {
  render(<StatusDot tone="warn">{words.dot}</StatusDot>);
  expect(host.textContent).toBe("98.2%");
  expect(host.querySelector("i")?.getAttribute("aria-hidden")).toBe("true");
});
