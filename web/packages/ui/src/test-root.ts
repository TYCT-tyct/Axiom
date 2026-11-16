import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach } from "vitest";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// testRoot gives each jsdom test a React root in a fresh element of the page, unmounted and removed after it. Call it
// once at the top of a test file and read host and root inside the tests.
export function testRoot() {
  const dom = { host: document.createElement("div"), root: undefined as unknown as Root };
  beforeEach(() => {
    dom.host = document.body.appendChild(document.createElement("div"));
    dom.root = createRoot(dom.host);
  });
  afterEach(() => {
    act(() => dom.root.unmount());
    dom.host.remove();
  });
  return dom;
}
