// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it } from "vitest";
import { DCLogic } from "./runtime.tsx";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

it("binds repeated ref callbacks in template order before lifecycle, and preserves nodes on update", async () => {
  const calls: string[] = [];
  let instance: Example | null = null;
  class Example extends DCLogic {
    override state = { label: "before" };
    receive = (node: Element) => {
      calls.push(node.id);
    };
    static override template: typeof DCLogic.template = (v, bind) => (
      <div id="parent" ref={bind("parent", v.receive as Example["receive"])}>
        <span id="first" ref={bind("first", v.receive as Example["receive"])}>
          {String(v.label)}
        </span>
        <span id="second" ref={bind("second", v.receive as Example["receive"])} />
      </div>
    );
    override renderVals() {
      return { receive: this.receive, label: this.state.label };
    }
    override componentDidMount() {
      this.flushRefs();
      calls.push("mounted");
    }
    override componentDidUpdate() {
      this.flushRefs();
    }
    override componentWillUnmount() {
      calls.push("unmounted");
    }
  }
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  await act(async () =>
    root.render(
      <Example
        ref={(el) => {
          instance = el;
        }}
      />,
    ),
  );
  expect(calls).toEqual(["parent", "first", "second", "mounted"]);
  const first = host.querySelector("#first");
  await act(async () => instance?.setState({ label: "after" }));
  expect(host.querySelector("#first")).toBe(first);
  expect(first?.textContent).toBe("after");
  expect(calls).toHaveLength(4);
  await act(async () => root.unmount());
  expect(calls.at(-1)).toBe("unmounted");
  host.remove();
});
