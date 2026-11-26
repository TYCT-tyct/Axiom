// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { PageTabs } from "./page-tabs";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root;
let host: HTMLElement;
const routed = vi.fn();

beforeEach(() => {
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  routed.mockClear();
  act(() =>
    root.render(
      <PageTabs
        label="sections"
        items={[
          { label: "users", href: "#users", current: false, onSelect: () => routed("users") },
          { label: "accounts", href: "#accounts", current: true, count: 3 },
          { label: "roles", current: false, onSelect: () => routed("roles") },
        ]}
      />,
    ),
  );
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const tabs = () => [...host.querySelectorAll<HTMLElement>('[role="tab"]')];
const press = (key: string) =>
  act(() => {
    document.activeElement?.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
  });

// One tab stop on the current tab; the arrows (wrapping), Home and End move focus without opening anything.
it("moves between tabs with the arrow keys and keeps one tab stop", () => {
  expect(host.querySelector('[role="tablist"]')?.getAttribute("aria-label")).toBe("sections");
  expect(tabs().map((tab) => tab.tabIndex)).toEqual([-1, 0, -1]);
  expect(tabs()[1]?.getAttribute("aria-selected")).toBe("true");
  expect(tabs()[1]?.textContent).toBe("accounts3");
  act(() => tabs()[1]?.focus());
  press("ArrowRight");
  expect(document.activeElement?.textContent).toBe("roles");
  press("ArrowRight");
  expect(document.activeElement?.textContent).toBe("users");
  press("ArrowLeft");
  expect(document.activeElement?.textContent).toBe("roles");
  press("Home");
  expect(document.activeElement?.textContent).toBe("users");
  press("End");
  expect(document.activeElement?.textContent).toBe("roles");
  expect(routed).not.toHaveBeenCalled();
});

// A link with onSelect is routed by it on a plain click, and left to the browser on a modified one.
it("routes a plain click through onSelect and leaves a modified click to the browser", () => {
  const plain = new MouseEvent("click", { bubbles: true, cancelable: true });
  act(() => tabs()[0]?.dispatchEvent(plain));
  expect(plain.defaultPrevented).toBe(true);
  const modified = new MouseEvent("click", { bubbles: true, cancelable: true, metaKey: true });
  act(() => tabs()[0]?.dispatchEvent(modified));
  expect(modified.defaultPrevented).toBe(false);
  act(() => tabs()[2]?.click());
  expect(routed.mock.calls).toEqual([["users"], ["roles"]]);
});
