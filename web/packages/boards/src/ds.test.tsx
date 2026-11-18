// @vitest-environment jsdom
import { act, type ReactNode, useState } from "react";
import { flushSync } from "react-dom";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  Dialog,
  Empty,
  ErrorState,
  FilterBar,
  Loading,
  LoadMore,
  NoPermission,
  SortableHeader,
  TextAreaField,
  TextField,
} from "./ds.tsx";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

let root: Root;
let host: HTMLElement;
beforeEach(() => {
  const shell = document.body.appendChild(document.createElement("div"));
  shell.className = "ax ov";
  const frame = shell.appendChild(document.createElement("div"));
  frame.className = "fr";
  host = frame.appendChild(document.createElement("main"));
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.closest(".ax")?.remove();
  vi.restoreAllMocks();
});

const render = (node: ReactNode) => act(() => root.render(node));
const element = <T extends HTMLElement>(selector: string) => document.querySelector<T>(selector) as T;
const click = (el: HTMLElement) => act(() => el.click());
const key = (el: Element, pressed: string, shiftKey = false) =>
  act(() =>
    el.dispatchEvent(new KeyboardEvent("keydown", { key: pressed, shiftKey, bubbles: true, cancelable: true })),
  );

it("keeps focus in a labelled dialog, closes on Escape and returns focus to the trigger", () => {
  vi.spyOn(HTMLElement.prototype, "getClientRects").mockReturnValue({ length: 1 } as DOMRectList);
  function Example() {
    const [open, setOpen] = useState(false);
    const close = () => setOpen(false);
    return (
      <>
        <button type="button" id="trigger" onClick={() => setOpen(true)}>
          {"Open"}
        </button>
        {open ? (
          <Dialog title="Edit" closeLabel="Dismiss" onClose={close} primary={{ label: "Save", onClick: close }}>
            <TextField name="name" label="Name" />
          </Dialog>
        ) : null}
      </>
    );
  }
  render(<Example />);
  const trigger = element("#trigger");
  trigger.focus();
  click(trigger);
  const dialog = element('[role="dialog"]');
  const input = element("input");
  const close = element(".nkx");
  const submit = element(".nkfoot button");
  expect(dialog.getAttribute("aria-modal")).toBe("true");
  expect(document.getElementById(dialog.getAttribute("aria-labelledby") ?? "")?.textContent).toBe("Edit");
  expect(document.activeElement).toBe(input);
  expect(host.hasAttribute("inert")).toBe(true);
  close.focus();
  key(close, "Tab", true);
  expect(document.activeElement).toBe(submit);
  key(submit, "Tab");
  expect(document.activeElement).toBe(close);
  key(close, "Escape");
  expect(document.querySelector('[role="dialog"]')).toBeNull();
  expect(document.activeElement).toBe(trigger);
});

it("keeps the scrollable body in the tab order and prevents a reload from submitting its replacement form", () => {
  const submit = vi.fn();
  function Example() {
    const [editing, setEditing] = useState(false);
    return (
      <Dialog
        title="Edit"
        closeLabel="Close"
        onClose={() => {}}
        primary={
          editing
            ? { label: "Save", form: "refilled" }
            : { label: "Reload", onClick: () => flushSync(() => setEditing(true)) }
        }
      >
        {editing ? (
          <form
            id="refilled"
            onSubmit={(event) => {
              event.preventDefault();
              submit();
            }}
          />
        ) : null}
      </Dialog>
    );
  }
  render(<Example />);
  expect(element(".nkb").tabIndex).toBe(0);
  const action = element<HTMLButtonElement>(".nkfoot button");
  click(action);
  expect(action.type).toBe("submit");
  expect(submit).not.toHaveBeenCalled();
  click(action);
  expect(submit).toHaveBeenCalledOnce();
});

it.each([
  [TextField, "input"],
  [TextAreaField, "textarea"],
] as const)(
  "%s associates the label and feedback, and retains the edited value when feedback changes",
  (Field, tag) => {
    render(<Field name="reason" label="Reason" hint="Give a reason" />);
    const control = element<HTMLInputElement>(tag);
    expect(control.labels?.[0]?.textContent).toBe("Reason");
    const described = () => document.getElementById(control.getAttribute("aria-describedby") ?? "")?.textContent;
    expect(described()).toBe("Give a reason");
    control.value = "Edited";
    render(<Field name="reason" label="Reason" hint="Give a reason" error="Try again" />);
    expect(control.getAttribute("aria-invalid")).toBe("true");
    expect(described()).toBe("Try again");
    expect(control.value).toBe("Edited");
  },
);

it("updates search, discloses filter choices with Escape and selects a labelled range", () => {
  const search = vi.fn();
  const select = vi.fn();
  const date = vi.fn();
  const range = { label: "Today", from: "2026-11-18", to: "2026-11-18" };
  render(
    <FilterBar
      label="Filters"
      search={{ label: "Search", placeholder: "Find", onChange: search }}
      selects={[
        {
          label: "Model",
          summary: "All models",
          allLabel: "All",
          options: [{ label: "A", value: "a" }],
          selected: [],
          onChange: select,
        },
      ]}
      dates={{ label: "Range", ranges: [range, range, range, range, range], selected: 0, onChange: date }}
    />,
  );
  const input = element<HTMLInputElement>('input[aria-label="Search"]');
  input.value = "model";
  act(() => input.dispatchEvent(new Event("input", { bubbles: true })));
  expect(search).toHaveBeenCalledWith("model");
  const toggle = element<HTMLButtonElement>('button[aria-label="Model"]');
  click(toggle);
  expect(toggle.getAttribute("aria-expanded")).toBe("true");
  const choice = element<HTMLButtonElement>("button[data-a=pickm]");
  expect(choice.getAttribute("aria-pressed")).toBe("false");
  click(choice);
  expect(select).toHaveBeenCalledWith(["a"]);
  key(choice, "Escape");
  expect(toggle.getAttribute("aria-expanded")).toBe("false");
  expect(document.activeElement).toBe(toggle);
  const today = element<HTMLButtonElement>('.qseg button[aria-pressed="true"]');
  click(today);
  expect(date).toHaveBeenCalledWith(range);
});

it("loads the next page once available and exposes loading and end states", () => {
  const load = vi.fn();
  render(<LoadMore state="ready" label="Load more" onLoadMore={load} />);
  click(element("button"));
  expect(load).toHaveBeenCalledOnce();
  render(<LoadMore state="loading" label="Loading" />);
  expect(element<HTMLButtonElement>("button").disabled).toBe(true);
  click(element("button"));
  expect(load).toHaveBeenCalledOnce();
  render(<LoadMore state="end" label="End of list" />);
  expect(element('[role="status"]').textContent).toBe("End of list");
});

it("exposes the active sort and lets keyboard users reverse it", () => {
  const sort = vi.fn();
  render(
    <table className="ust">
      <thead>
        <tr>
          <SortableHeader direction="descending" onSort={sort}>
            {"Cost"}
          </SortableHeader>
        </tr>
      </thead>
    </table>,
  );
  const header = element("th");
  expect(header.tabIndex).toBe(0);
  expect(header.getAttribute("aria-sort")).toBe("descending");
  key(header, "Enter");
  key(header, " ");
  click(header);
  expect(sort.mock.calls).toEqual([["ascending"], ["ascending"], ["ascending"]]);
});

it("labels the loading region as busy", () => {
  render(
    <Loading label="Loading records">
      <p>{"Reserved space"}</p>
    </Loading>,
  );
  const status = element('[role="status"]');
  expect(status.getAttribute("aria-label")).toBe("Loading records");
  expect(status.getAttribute("aria-busy")).toBe("true");
});

it.each([Empty, NoPermission])("%s announces its message and can offer an action", (State) => {
  const actOnState = vi.fn();
  render(<State title="Nothing to show" action={{ label: "Go back", onClick: actOnState }} />);
  expect(element('[role="status"] h2').textContent).toBe("Nothing to show");
  click(element("button"));
  expect(actOnState).toHaveBeenCalledOnce();
});

it("announces the error and retries using a button", () => {
  const retry = vi.fn();
  render(<ErrorState title="Could not load" retry={{ label: "Retry", onClick: retry }} />);
  expect(element('[role="alert"] h2').textContent).toBe("Could not load");
  click(element("button"));
  expect(retry).toHaveBeenCalledOnce();
});
