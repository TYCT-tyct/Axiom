// @vitest-environment jsdom
// The pieces a settings page is edited with (admin_set.js): NumberField, ChipField, ReasonPicks, SettingRow and
// UnsavedBar, each held in state as a page holds it.
import { act, type ReactNode, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ChipField } from "./chip-field";
import { NumberField } from "./number-field";
import { ReasonPicks } from "./reason-picks";
import { SettingRow } from "./setting-row";
import { UnsavedBar } from "./unsaved-bar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const host = document.body.appendChild(document.createElement("div"));
let root: Root;
beforeEach(() => {
  root = createRoot(host);
});
afterEach(() => act(() => root.unmount()));

const render = (node: ReactNode) => act(() => root.render(node));
const key = (target: Element, init: KeyboardEventInit) =>
  act(() => target.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init })));
const button = (name: string) =>
  [...host.querySelectorAll("button")].find((b) => (b.getAttribute("aria-label") ?? b.textContent) === name);

function HeldNumber({
  start,
  ...props
}: {
  start: string;
  decimals?: number;
  step?: number;
  min?: number;
  max?: number;
}) {
  const [value, setValue] = useState(start);
  return <NumberField label="limit" unit="req/min" value={value} onChange={setValue} {...props} />;
}

// Up and down step by one unit of the last decimal, Shift by ten, within min and max, written with the decimals; a
// value that is not a number is left as typed.
it.each([
  { start: "5", props: {}, keys: [["ArrowUp"], ["ArrowUp", true], ["ArrowDown"]], want: ["6", "16", "15"] },
  { start: "0.10", props: { decimals: 2 }, keys: [["ArrowUp"], ["ArrowDown", true]], want: ["0.11", "0.01"] },
  { start: "3", props: { min: 0, max: 10 }, keys: [["ArrowDown", true]], want: ["0"] },
  { start: "9", props: { min: 0, max: 10 }, keys: [["ArrowUp", true]], want: ["10"] },
  { start: "2", props: { step: 5 }, keys: [["ArrowUp"]], want: ["7"] },
  { start: "abc", props: {}, keys: [["ArrowUp"]], want: ["abc"] },
  { start: "", props: {}, keys: [["ArrowUp"]], want: [""] },
] as const)("NumberField steps $start with $props", ({ start, props, keys, want }) => {
  render(<HeldNumber start={start} {...props} />);
  const input = host.querySelector("input") as HTMLInputElement;
  const values = keys.map(([name, shift]) => {
    key(input, { key: name, shiftKey: shift ?? false });
    return input.value;
  });
  expect(values).toEqual(want);
});

function HeldChips() {
  const [values, setValues] = useState(["a.com"]);
  return <ChipField label="domains" values={values} onChange={setValues} removeLabel={(value) => `remove ${value}`} />;
}

// Enter or a comma (either width) adds the trimmed text and clears the box; empty or repeated text adds nothing; ×
// takes its chip out and leaves focus in the box.
it("ChipField adds chips on Enter or comma and removes them with ×", () => {
  render(<HeldChips />);
  const input = host.querySelector("input") as HTMLInputElement;
  const type = (text: string, end: string) => {
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(input, text);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    key(input, { key: end });
  };
  type(" b.com ", "Enter");
  type("c.com", ",");
  type("d.com", "，");
  type("a.com", "Enter");
  type("   ", "Enter");
  const chips = () => [...host.querySelectorAll("span")].map((chip) => chip.textContent);
  expect(chips()).toEqual(["a.com", "b.com", "c.com", "d.com"]);
  expect(input.value).toBe("");
  act(() => button("remove b.com")?.click());
  expect(chips()).toEqual(["a.com", "c.com", "d.com"]);
  expect(document.activeElement).toBe(input);
});

function HeldReason() {
  const [value, setValue] = useState("");
  return (
    <ReasonPicks
      label="reason"
      picksLabel="common"
      picks={["upkeep", "user asked"]}
      value={value}
      onChange={setValue}
    />
  );
}

// A chip fills the box with its words (replacing another chip's), leaves the box alone when they are already in it, and
// hands focus to the box.
it("ReasonPicks fills the textarea from a reason chip", () => {
  render(<HeldReason />);
  const box = host.querySelector("textarea") as HTMLTextAreaElement;
  const values = ["upkeep", "user asked", "user asked"].map((name) => {
    act(() => button(name)?.click());
    return box.value;
  });
  expect(values).toEqual(["upkeep", "user asked", "user asked"]);
  expect(document.activeElement).toBe(box);
});

// The row's undo is out of reach until the row is dirty; a flash lights the row once and goes when its animation ends.
it("SettingRow offers its undo only while dirty and flashes once", () => {
  const onReset = vi.fn();
  const row = (dirty: boolean, flash: boolean) => (
    <SettingRow title="limit" dirty={dirty} flash={flash} reset={{ label: "undo", name: "undo limit", onReset }}>
      <input aria-label="limit" />
    </SettingRow>
  );
  render(row(false, false));
  const reset = button("undo limit") as HTMLButtonElement;
  expect([reset.tabIndex, reset.getAttribute("aria-hidden")]).toEqual([-1, "true"]);
  render(row(true, true));
  expect([reset.tabIndex, reset.getAttribute("aria-hidden")]).toEqual([0, null]);
  const element = host.firstElementChild as HTMLElement;
  const lit = element.className;
  // jsdom has no AnimationEvent, so React listens for the prefixed name
  act(() => element.dispatchEvent(new Event("webkitAnimationEnd", { bubbles: true })));
  expect(element.className).not.toBe(lit);
  render(row(true, true));
  expect(element.className).not.toBe(lit);
  act(() => reset.click());
  expect(onReset).toHaveBeenCalledTimes(1);
});

// While closed the bar is inert and Cmd+S is the browser's; open, it submits, unless the submit is disabled or a dialog
// is open over the page.
it("UnsavedBar submits on Cmd+S only while open, enabled and under no dialog", () => {
  const submit = vi.fn();
  const bar = (open: boolean, submitDisabled = false) => (
    <UnsavedBar
      open={open}
      message="2 changes"
      discard={{ label: "discard", onClick: () => {} }}
      submit={{ label: "review", onClick: submit }}
      submitDisabled={submitDisabled}
      shortcut="⌘S"
    />
  );
  const save = () => {
    const event = new KeyboardEvent("keydown", { key: "s", metaKey: true, bubbles: true, cancelable: true });
    act(() => document.body.dispatchEvent(event));
    return event.defaultPrevented;
  };
  render(bar(false));
  expect([host.firstElementChild?.hasAttribute("inert"), save()]).toEqual([true, false]);
  render(bar(true));
  expect([host.firstElementChild?.hasAttribute("inert"), save()]).toEqual([false, true]);
  expect(submit).toHaveBeenCalledTimes(1);
  const dialog = document.body.appendChild(document.createElement("dialog"));
  dialog.setAttribute("open", "");
  expect(save()).toBe(false);
  dialog.remove();
  render(bar(true, true));
  expect(save()).toBe(true);
  expect(submit).toHaveBeenCalledTimes(1);
});
