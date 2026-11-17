// @vitest-environment jsdom
import { setImmediate } from "node:timers/promises";
import type { Page } from "@playwright/test";
import { afterEach, expect, it, vi } from "vitest";
import { bind, render } from "../e2e/design/board.ts";
import { usedFaces } from "../e2e/design/fonts.ts";
import { canvasesReady, stage } from "../e2e/design/stage.ts";

vi.mock("../packages/boards/fonts.ts", () => ({ sourceFont: vi.fn() }));

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  document.body.replaceChildren();
  Reflect.deleteProperty(window, "AxiomOceanOrig");
});

it("binds reference-board event holes alongside refs, including each repeated item", () => {
  const root = document.createElement("div");
  const receive = vi.fn();
  const click = vi.fn();
  const input = vi.fn();
  const refs: unknown[] = [];
  root.innerHTML = render(
    '<div ref="{{ root }}"><sc-for list="{{ items }}" as="item"><button onClick="{{ item.click }}">{{ item.label }}</button></sc-for><input onInput="{{ input }}"></div>',
    {
      root: receive,
      items: [
        { label: "one", click },
        { label: "two", click },
      ],
      input,
    },
    refs,
  );
  bind(root, refs);
  expect(receive).toHaveBeenCalledWith(root.firstElementChild);
  for (const button of root.querySelectorAll("button")) button.click();
  root.querySelector("input")?.dispatchEvent(new Event("input"));
  expect(click).toHaveBeenCalledTimes(2);
  expect(input).toHaveBeenCalledOnce();
});

it("waits for both native canvas observers before advancing scene time", async () => {
  document.body.innerHTML = "<canvas></canvas>";
  let resize = () => {};
  let intersect = () => {};
  const disconnect = vi.fn();
  vi.stubGlobal(
    "ResizeObserver",
    class {
      constructor(callback: () => void) {
        resize = callback;
      }
      observe() {}
      disconnect = disconnect;
    },
  );
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      constructor(callback: () => void) {
        intersect = callback;
      }
      observe() {}
      disconnect = disconnect;
    },
  );
  const page = { evaluate: async (fn: () => unknown) => fn() } as unknown as Page;
  let ready = false;
  const pending = canvasesReady(page).then(() => {
    ready = true;
  });
  resize();
  await setImmediate();
  expect(ready).toBe(false);
  intersect();
  await pending;
  expect(ready).toBe(true);
  expect(disconnect).toHaveBeenCalledTimes(2);
});

it("checks text input fonts without treating checkbox chrome as text", async () => {
  document.body.innerHTML = '<input type="checkbox"><input type="text">';
  vi.spyOn(window, "getComputedStyle").mockImplementation((element, pseudo) => {
    return {
      fontFamily: element.getAttribute("type") === "checkbox" ? "Arial" : '"Axiom Sans"',
      content: pseudo ? "none" : "normal",
      display: "inline-block",
    } as CSSStyleDeclaration;
  });
  const page = { evaluate: async (fn: () => unknown) => fn() } as unknown as Page;
  const face = { family: "Axiom Sans", style: "normal", weight: "400", sample: "Axiom" };
  expect(await usedFaces(page, [face])).toEqual([face]);
  document.body.innerHTML = '<input type="text" style="font-family:Missing">';
  vi.mocked(window.getComputedStyle).mockReturnValue({ fontFamily: "Missing", content: "none" } as CSSStyleDeclaration);
  await expect(usedFaces(page, [face])).rejects.toThrow("the board uses the font Missing");
});

it("starts identical ocean worlds despite framework randomness before mounting", async () => {
  const random = Math.random;
  const page = {
    addInitScript: async (fn: (seed: number) => void, seed?: number) => {
      if (seed !== undefined) fn(seed);
    },
    clock: { install: async () => {} },
  } as unknown as Page;
  const worlds: number[][] = [];
  try {
    for (const draws of [0, 2, 5]) {
      await stage(page, {
        viewport: { width: 1440, height: 900 },
        colorScheme: "light",
        timezoneId: "Asia/Shanghai",
        time: new Date("2026-10-24T04:00:00Z"),
      });
      for (let i = 0; i < draws; i++) Math.random();
      const ocean = {
        Engine: class {
          fish = [Math.random(), Math.random(), Math.random()];
        },
      };
      Object.assign(window, { AxiomOceanOrig: ocean });
      worlds.push(new ocean.Engine().fish);
    }
    expect(worlds[1]).toEqual(worlds[0]);
    expect(worlds[2]).toEqual(worlds[0]);
  } finally {
    Math.random = random;
  }
});
