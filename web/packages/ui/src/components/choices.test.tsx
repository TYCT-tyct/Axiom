// @vitest-environment jsdom
import { act, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it } from "vitest";
import { DataTable } from "./data-table";
import { Disclosure } from "./disclosure";
import { StepList } from "./step-list";

// The upstream pages' parts: a channel's checklist, a pile that reads what it holds
// only once opened, and a table row that opens in place. None has words of its own, so no translation is mounted.
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

// What the last render drew into, and the root to draw it again with other props.
let host = document.createElement("div");
function render(node: ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  act(() => root.render(node));
  return root;
}

afterEach(() => document.body.replaceChildren());

it("ticks the steps done, marks the first not done as current and shows only its action", () => {
  const action = <button type="button">{"go"}</button>;
  render(
    <StepList
      label="Setup"
      steps={[
        { key: "a", title: "Connect", done: true, action },
        { key: "b", title: "Keys", done: false, action },
        { key: "c", title: "Unit", done: false, action },
      ]}
    />,
  );
  const steps = [...host.querySelectorAll("li")];
  expect(steps.map((step) => step.getAttribute("aria-current"))).toEqual([null, "step", null]);
  expect(steps[2]?.textContent).toContain("3");
  expect(host.querySelectorAll("button")).toHaveLength(1);
});

it("draws a lazy pile's content only once it is first opened, and counts it in the summary", () => {
  const Held = () => <p>{"held"}</p>;
  render(
    <Disclosure summary="Listed" count="12" note="live" lazy>
      <Held />
    </Disclosure>,
  );
  const details = host.querySelector("details") as HTMLDetailsElement;
  expect(details.querySelector("em")?.textContent).toBe("12");
  expect(host.textContent).not.toContain("held");
  act(() => {
    details.open = true;
    details.dispatchEvent(new Event("toggle"));
  });
  expect(host.textContent).toContain("held");
});

it("opens a row in place under it and says it is expanded", () => {
  type Row = { id: string };
  const rows: Row[] = [{ id: "a" }, { id: "b" }];
  const table = (expanded?: string) => (
    <DataTable<Row>
      label="rows"
      rows={rows}
      rowKey={(row) => row.id}
      columns={[{ key: "id", header: "id", cell: (row) => row.id }]}
      onOpen={() => {}}
      detail={(row) => `lines of ${row.id}`}
      expanded={expanded}
    />
  );
  const root = render(table());
  expect(host.querySelectorAll("tbody tr")).toHaveLength(2);
  act(() => root.render(table("b")));
  const body = [...host.querySelectorAll("tbody tr")];
  expect(body).toHaveLength(3);
  expect(body[2]?.textContent).toBe("lines of b");
  expect(body[1]?.querySelector("button")?.getAttribute("aria-expanded")).toBe("true");
  expect(body[0]?.querySelector("button")?.getAttribute("aria-expanded")).toBe("false");
});
