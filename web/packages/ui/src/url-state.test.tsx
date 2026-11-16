// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it } from "vitest";
import { useUrlParams, useUrlSelection } from "./url-state";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const defaults = { q: "", status: "all" };
const allowed = { status: ["all", "ok", "failed"] };
let seen: ReturnType<typeof useUrlParams<"q" | "status">>;
let selection: ReturnType<typeof useUrlSelection>;
let root: Root;
let host: HTMLElement;

function Probe() {
  seen = useUrlParams(defaults, allowed);
  selection = useUrlSelection();
  return null;
}

beforeEach(() => {
  window.history.replaceState(null, "", "/logs");
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  act(() => root.render(<Probe />));
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

it("keeps what changes in the address and leaves defaults out of it", () => {
  act(() => seen[1]({ q: "gpt", status: "failed" }));
  expect(window.location.search).toBe("?q=gpt&status=failed");
  expect(seen[0]).toEqual({ q: "gpt", status: "failed" });
  act(() => seen[1]({ q: "", status: "all" }));
  expect(window.location.search).toBe("");
  expect(seen[0]).toEqual(defaults);
});

it("reads a value outside its allowed set as the default", () => {
  act(() => {
    window.history.replaceState(null, "", "/logs?status=weird&q=a");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  expect(seen[0]).toEqual({ q: "a", status: "all" });
});

it("adds a history entry for a chosen row, rewrites it for arrow keys, and follows Back", () => {
  const before = window.history.length;
  act(() => selection.onSelect("a", "pointer"));
  expect(window.history.length).toBe(before + 1);
  act(() => selection.onSelect("b", "key"));
  expect(window.history.length).toBe(before + 1);
  expect(selection.selected).toBe("b");
  act(() => selection.onSelect(null, "close"));
  expect(selection.selected).toBeNull();
  act(() => {
    window.history.back();
  });
  return new Promise<void>((done) =>
    window.addEventListener("popstate", () => {
      expect(window.location.search).toBe("?selected=b");
      done();
    }),
  );
});
