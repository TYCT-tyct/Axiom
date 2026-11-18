// @vitest-environment jsdom
import { act, type ReactNode, useEffect, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  ActionButton,
  CheckboxField,
  CodeBlock,
  CopyButton,
  FileField,
  FilterForm,
  SelectField,
  SuggestionField,
  TestProgress,
} from "./ds.tsx";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

let root: Root;
let host: HTMLElement;

beforeEach(() => {
  host = document.body.appendChild(document.createElement("div"));
  host.className = "ax ov";
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const render = (node: ReactNode) => act(() => root.render(node));
const element = <T extends HTMLElement>(selector: string) => host.querySelector<T>(selector) as T;
const click = (node: HTMLElement) => act(() => node.click());
const input = (node: HTMLInputElement | HTMLSelectElement, value: string) =>
  act(() => {
    node.value = value;
    node.dispatchEvent(new Event("input", { bubbles: true }));
  });
const formValues = () => Object.fromEntries(new FormData(element<HTMLFormElement>("form")));

it("keeps drafts out of the address until submit, restores URL changes, and resets even an empty address", () => {
  const address = () => new URLSearchParams(location.search);
  const read = () => Object.fromEntries(address());
  const navigate = vi.fn((values: Record<string, string>) => {
    const search = new URLSearchParams(Object.entries(values).filter(([, value]) => value));
    history.pushState(null, "", search.size ? `?${search}` : "/");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  function Route() {
    const [applied, setApplied] = useState(read);
    useEffect(() => {
      const restore = () => setApplied(read());
      window.addEventListener("popstate", restore);
      return () => window.removeEventListener("popstate", restore);
    }, []);
    return (
      <FilterForm
        label="Order filters"
        values={applied}
        fields={[
          { type: "text", name: "account", label: "Account" },
          {
            type: "select",
            name: "state",
            label: "State",
            options: [
              { value: "", label: "Any" },
              { value: "paid", label: "Paid" },
            ],
          },
        ]}
        range={{ from: { name: "since", label: "From" }, until: { name: "until", label: "Until" } }}
        applyLabel="Apply"
        resetLabel="Reset"
        onApply={navigate}
      />
    );
  }
  const date = "2026-11-18T12:30";
  history.replaceState(null, "", "?account=original");
  render(<Route />);
  const account = () => element<HTMLInputElement>("input[name=account]");
  expect(account().value).toBe("original");
  input(account(), "edited");
  input(element<HTMLSelectElement>("select"), "paid");
  input(element<HTMLInputElement>("[name=since]"), date);
  input(element<HTMLInputElement>("[name=until]"), "2026-11-19T12:30");
  expect(navigate).not.toHaveBeenCalled();
  expect(address().get("account")).toBe("original");
  click(element("button[type=submit]"));
  expect(read()).toEqual({ account: "edited", state: "paid", since: date, until: "2026-11-19T12:30" });

  act(() => {
    history.replaceState(null, "", "?account=restored");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  expect(account().value).toBe("restored");
  expect(element<HTMLSelectElement>("select").value).toBe("");
  const reset = () => click(element("button[type=button]"));
  reset();
  expect(address().size).toBe(0);
  input(account(), "draft without applied filters");
  reset();
  expect(account().value).toBe("");
  expect(navigate).toHaveBeenLastCalledWith({});
});

it("keeps a native select controlled and submits the selected value", () => {
  const changed = vi.fn();
  const view = (value: string) => (
    <form>
      <SelectField
        label="Preset"
        name="preset"
        value={value}
        onChange={changed}
        error="Choose an available preset"
        options={[
          { value: "one", label: "One" },
          { value: "two", label: "Two" },
        ]}
      />
    </form>
  );
  render(view("one"));
  const control = element<HTMLSelectElement>("select");
  control.focus();
  expect(document.activeElement).toBe(control);
  expect(control.labels?.[0]?.textContent).toBe("Preset");
  expect(control.getAttribute("aria-invalid")).toBe("true");
  input(control, "two");
  expect(changed).toHaveBeenCalledWith("two");
  expect(formValues()).toEqual({ preset: "one" });
  render(view("two"));
  expect(formValues()).toEqual({ preset: "two" });
});

it("uses a focused checkbox button and submits only the checked value", () => {
  function Example() {
    const [checked, setChecked] = useState(false);
    return (
      <form>
        <CheckboxField label="Published" name="published" checked={checked} onChange={setChecked} />
      </form>
    );
  }
  render(<Example />);
  const control = element<HTMLButtonElement>("[role=checkbox]");
  control.focus();
  expect(document.activeElement).toBe(control);
  expect(control.getAttribute("aria-checked")).toBe("false");
  expect(formValues()).toEqual({});
  click(control);
  expect(control.getAttribute("aria-checked")).toBe("true");
  expect(formValues()).toEqual({ published: "on" });
});

it("associates the browser suggestion list with the labelled text field", () => {
  const changed = vi.fn();
  render(
    <SuggestionField label="Model" name="model" value="" onChange={changed} suggestions={["cloud-a", "cloud-b"]} />,
  );
  const control = element<HTMLInputElement>("input");
  expect(control.labels?.[0]?.textContent).toBe("Model");
  expect(Array.from(control.list?.options ?? [], (option) => option.value)).toEqual(["cloud-a", "cloud-b"]);
  input(control, "cloud-b");
  expect(changed).toHaveBeenCalledWith("cloud-b");
});

it("opens the native file picker from the visible action and announces the selected name", () => {
  const changed = vi.fn();
  render(<FileField label="Choose logo" name="logo" accept="image/png" hint="PNG image" onChange={changed} />);
  const control = element<HTMLInputElement>("input");
  const visible = element<HTMLButtonElement>("button");
  const picker = vi.spyOn(control, "click");
  visible.focus();
  expect(document.activeElement).toBe(visible);
  expect(document.getElementById(visible.getAttribute("aria-describedby") ?? "")?.textContent).toBe("PNG image");
  click(visible);
  expect(picker).toHaveBeenCalledOnce();
  expect(control.accept).toBe("image/png");
  const file = new File(["image"], "logo.png", { type: "image/png" });
  Object.defineProperty(control, "files", { value: [file] });
  act(() => control.dispatchEvent(new Event("change", { bubbles: true })));
  expect(changed).toHaveBeenCalledWith(file);
  expect(element("[role=status]").textContent).toBe("logo.png");
});

it.each([
  ["primary", "pri"],
  ["secondary", "sec"],
  ["danger", "dan2"],
  ["link", "qlnk"],
] as const)("renders the %s action and prevents presses while unavailable", (variant, mark) => {
  const press = vi.fn();
  const view = (busy = false, disabled = false) => (
    <ActionButton variant={variant} busy={busy} disabled={disabled} onClick={press}>
      {"Action"}
    </ActionButton>
  );
  render(view());
  const button = element<HTMLButtonElement>("button");
  button.focus();
  expect(document.activeElement).toBe(button);
  expect(button.classList.contains(mark)).toBe(true);
  click(button);
  render(view(true));
  expect(button.getAttribute("aria-busy")).toBe("true");
  click(button);
  render(view(false, true));
  expect(button.hasAttribute("aria-busy")).toBe(false);
  click(button);
  expect(button.disabled).toBe(true);
  expect(press).toHaveBeenCalledOnce();
});

it("puts unchanged, labelled code in the tab order for native scrolling", () => {
  const text = '{\n  "id": 9007199254740993\n}';
  render(<CodeBlock label="Audit detail">{text}</CodeBlock>);
  const code = element("pre[role=region]");
  expect(code.getAttribute("aria-label")).toBe("Audit detail");
  expect(code.tabIndex).toBe(0);
  code.focus();
  expect(document.activeElement).toBe(code);
  expect(code.textContent).toBe(text);
});

it("reports clipboard success and failure without replacing the focused button", async () => {
  const writeText = vi.fn().mockResolvedValueOnce(undefined).mockRejectedValueOnce(new Error("refused"));
  vi.stubGlobal("navigator", { clipboard: { writeText } });
  render(<CopyButton text="request-id" label="Copy ID" copied="Copied" failed="Copy failed" />);
  const button = element<HTMLButtonElement>("button");
  button.focus();
  await act(async () => button.click());
  expect(writeText).toHaveBeenCalledWith("request-id");
  expect(element("[aria-live]").textContent).toBe("Copied");
  expect(document.activeElement).toBe(button);
  await act(async () => button.click());
  expect(element("[aria-live]").textContent).toBe("Copy failed");
});

it("updates elapsed time, caps the deadline meter, and keeps the waiting announcement steady", () => {
  vi.useFakeTimers();
  render(<TestProgress label="Running test" limitSeconds={5} formatElapsed={(seconds) => `${seconds}s`} />);
  const meter = element("[role=meter]");
  expect(meter.getAttribute("aria-valuenow")).toBe("0");
  act(() => vi.advanceTimersByTime(7_000));
  expect(meter.getAttribute("aria-valuenow")).toBe("5");
  expect(meter.getAttribute("aria-valuetext")).toBe("7s");
  expect(element("[role=status]").textContent).toBe("Running test");
});
