// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { Menu } from "./menu";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root;
let host: HTMLElement;
const chosen = vi.fn();

beforeEach(() => {
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  chosen.mockClear();
  act(() =>
    root.render(
      <Menu
        label="more"
        trigger={(props) => (
          <button type="button" {...props}>
            {"open"}
          </button>
        )}
        items={["one", "two", "three"].map((label) => ({ label, onSelect: () => chosen(label) }))}
      />,
    ),
  );
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const trigger = () => host.querySelector<HTMLButtonElement>("[aria-haspopup]") as HTMLButtonElement;
const focused = () => document.activeElement?.textContent;
const press = (key: string) =>
  act(() => {
    document.activeElement?.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
  });
const open = () => act(() => trigger().click());

it("opens a named menu with the first item focused", () => {
  expect(trigger().getAttribute("aria-expanded")).toBe("false");
  open();
  const menu = host.querySelector('[role="menu"]');
  expect(menu?.getAttribute("aria-label")).toBe("more");
  expect(trigger().getAttribute("aria-expanded")).toBe("true");
  expect(trigger().getAttribute("aria-controls")).toBe(menu?.id);
  expect([...host.querySelectorAll('[role="menuitem"]')].map((item) => item.textContent)).toEqual([
    "one",
    "two",
    "three",
  ]);
  expect(focused()).toBe("one");
});

it.each([
  { keys: ["ArrowDown"], want: "two" },
  { keys: ["ArrowDown", "ArrowDown", "ArrowDown"], want: "one" },
  { keys: ["ArrowUp"], want: "three" },
  { keys: ["End"], want: "three" },
  { keys: ["End", "Home"], want: "one" },
])("moves the focus on $keys", ({ keys, want }) => {
  open();
  for (const key of keys) {
    press(key);
  }
  expect(focused()).toBe(want);
});

it("closes on Esc and gives the focus back to the trigger", () => {
  open();
  press("ArrowDown");
  press("Escape");
  expect(host.querySelector('[role="menu"]')).toBeNull();
  expect(trigger().getAttribute("aria-expanded")).toBe("false");
  expect(document.activeElement).toBe(trigger());
  expect(chosen).not.toHaveBeenCalled();
});

it("acts on the chosen item, closes and gives the focus back", () => {
  open();
  press("ArrowDown");
  act(() => (document.activeElement as HTMLElement).click());
  expect(chosen).toHaveBeenCalledWith("two");
  expect(host.querySelector('[role="menu"]')).toBeNull();
  expect(document.activeElement).toBe(trigger());
});

it("closes on a press outside without taking the focus", () => {
  open();
  act(() => {
    document.body.dispatchEvent(new Event("pointerdown", { bubbles: true }));
  });
  expect(host.querySelector('[role="menu"]')).toBeNull();
});
