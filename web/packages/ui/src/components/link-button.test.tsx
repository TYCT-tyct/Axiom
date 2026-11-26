// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { LinkButton } from "./link-button";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

it("is a button that does something and does not submit a form", () => {
  const host = document.body.appendChild(document.createElement("div"));
  const root = createRoot(host);
  const open = vi.fn();
  act(() =>
    root.render(
      <LinkButton label="Open openai/model-1" onClick={open}>
        {"openai/model-1"}
      </LinkButton>,
    ),
  );
  const button = host.querySelector("button");
  expect(button?.type).toBe("button");
  expect(button?.getAttribute("aria-label")).toBe("Open openai/model-1");
  act(() => button?.click());
  expect(open).toHaveBeenCalledOnce();
  act(() => root.unmount());
  host.remove();
});
