// @vitest-environment jsdom
import { act, type ReactNode, useRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useFrameBehaviour } from "./behaviour";
import { useFitFrame } from "./script";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let currentLang = "zh";
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ i18n: { language: currentLang } }),
}));

type IOCb = (entries: IntersectionObserverEntry[]) => void;

function FrameHarness({ wide }: { wide: boolean }) {
  const root = useRef<HTMLDivElement>(null);
  useFrameBehaviour(root, wide);
  return (
    <div ref={root} className="tr site">
      <div className="ho">
        <div className="depth" />
        <div className="hs">
          <div className="under" />
        </div>
      </div>
    </div>
  );
}

function FitStage({ stage, narrow }: { stage: HTMLElement | null; narrow: boolean }) {
  useFitFrame(stage, narrow);
  return null;
}

describe("home desktop inertial wheel and narrow frame height", () => {
  const rafs = new Map<number, FrameRequestCallback>();
  const mounted: { root: Root; host: HTMLElement }[] = [];
  let nextId = 1;
  let ioCb: IOCb | undefined;
  let resizeCb: ResizeObserverCallback | undefined;

  const step = (now: number) => {
    const batch = [...rafs.entries()];
    rafs.clear();
    for (const [, fn] of batch) fn(now);
  };

  function mount(node: ReactNode) {
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    mounted.push({ root, host });
    act(() => root.render(node));
    return {
      host,
      rerender: (next: ReactNode) => act(() => root.render(next)),
      unmount: () => act(() => root.unmount()),
    };
  }

  beforeEach(() => {
    currentLang = "zh";
    rafs.clear();
    nextId = 1;
    vi.stubGlobal("requestAnimationFrame", (fn: FrameRequestCallback) => {
      const id = nextId++;
      rafs.set(id, fn);
      return id;
    });
    vi.stubGlobal("cancelAnimationFrame", (id: number) => rafs.delete(id));
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        constructor(cb: IOCb) {
          ioCb = cb;
        }
        observe(target: Element) {
          ioCb?.([{ target, isIntersecting: true } as IntersectionObserverEntry]);
        }
        unobserve() {}
        disconnect() {}
      },
    );
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(cb: ResizeObserverCallback) {
          resizeCb = cb;
        }
        observe() {}
        unobserve() {}
        disconnect() {}
      },
    );
  });

  afterEach(() => {
    while (mounted.length > 0) {
      const item = mounted.pop();
      if (item) {
        act(() => item.root.unmount());
        item.host.remove();
      }
    }
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
  });

  function setupScroller(scroller: HTMLElement, integerRounding = true) {
    let top = 0;
    Object.defineProperty(scroller, "scrollHeight", { value: 4000, configurable: true });
    Object.defineProperty(scroller, "clientHeight", { value: 900, configurable: true });
    Object.defineProperty(scroller, "scrollTop", {
      configurable: true,
      get: () => top,
      set: (v: number) => {
        const clamped = Math.max(0, Math.min(3100, v));
        top = integerRounding ? Math.floor(clamped) : clamped;
      },
    });
    scroller.scrollTo = vi.fn();
  }

  it("eases wheel scroll smoothly on wide, accumulates ticks, handles integer scrollTop, and releases loop", () => {
    const { host } = mount(<FrameHarness wide={true} />);
    const scroller = host.querySelector<HTMLElement>(".hs") as HTMLElement;
    setupScroller(scroller, true);

    const first = new WheelEvent("wheel", { deltaY: 200, bubbles: true, cancelable: true });
    scroller.dispatchEvent(first);
    expect(first.defaultPrevented).toBe(true);
    expect(window.axiomFx?.state.owners).toBe(1);

    // First frame moves smoothly partway, not jumping directly to 200.
    step(0);
    expect(scroller.scrollTop).toBeGreaterThan(0);
    expect(scroller.scrollTop).toBeLessThan(200);

    // Second wheel accumulates onto the target (200 + 200 = 400).
    scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: 200, bubbles: true, cancelable: true }));
    for (let t = 16; t <= 2000; t += 16) step(t);
    expect(scroller.scrollTop).toBe(400);
    expect(window.axiomFx?.state.owners).toBe(0);
    expect(window.axiomFx?.state.pending).toBe(false);
  });

  it("ignores ctrl+wheel and narrow layout, and stops on pointerdown or external scrollTop jump", () => {
    const narrowView = mount(<FrameHarness wide={false} />);
    const narrowScroller = narrowView.host.querySelector<HTMLElement>(".hs") as HTMLElement;
    setupScroller(narrowScroller, false);
    const narrowWheel = new WheelEvent("wheel", { deltaY: 120, bubbles: true, cancelable: true });
    narrowScroller.dispatchEvent(narrowWheel);
    expect(narrowWheel.defaultPrevented).toBe(false);
    expect(window.axiomFx?.state.owners).toBe(0);
    narrowView.unmount();

    const wideView = mount(<FrameHarness wide={true} />);
    const scroller = wideView.host.querySelector<HTMLElement>(".hs") as HTMLElement;
    const frame = wideView.host.querySelector<HTMLElement>(".tr") as HTMLElement;
    setupScroller(scroller, false);

    // Pinch-zoom (ctrlKey) must pass through untouched.
    const pinch = new WheelEvent("wheel", { deltaY: 100, ctrlKey: true, bubbles: true, cancelable: true });
    scroller.dispatchEvent(pinch);
    expect(pinch.defaultPrevented).toBe(false);

    // Pointerdown stops inertial easing and releases the loop.
    scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: 300, bubbles: true, cancelable: true }));
    step(0);
    expect(window.axiomFx?.state.owners).toBe(1);
    frame.dispatchEvent(new Event("pointerdown", { bubbles: true }));
    expect(window.axiomFx?.state.owners).toBe(0);

    // External scrollTop jump cancels easing cleanly.
    scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: 300, bubbles: true, cancelable: true }));
    step(16);
    scroller.scrollTop = 900;
    step(32);
    expect(scroller.scrollTop).toBe(900);
    expect(window.axiomFx?.state.owners).toBe(0);
  });

  it("sizes narrow steps/trust frames to measured section height + 96 and tracks tablet/language changes", async () => {
    const ho = document.createElement("div");
    ho.className = "ho";
    const frame = document.createElement("div");
    frame.className = "frame";
    frame.style.height = "1600px";
    const sec = document.createElement("section");
    sec.className = "sec";
    frame.append(sec);
    ho.append(frame);
    document.body.append(ho);

    let naturalHeight = 1720;
    Object.defineProperty(sec, "offsetHeight", {
      configurable: true,
      get: () => (sec.style.position === "relative" && sec.style.height === "auto" ? naturalHeight : 1600),
    });

    const view = mount(<FitStage stage={frame} narrow={true} />);
    expect(frame.style.height).toBe("1816px");
    expect(sec.style.position).toBe("");
    expect(sec.style.height).toBe("");

    // Switching to tablet layout (data-tab) shrinks the natural height and updates the frame height.
    naturalHeight = 980;
    await act(async () => {
      ho.setAttribute("data-tab", "");
      await Promise.resolve();
    });
    expect(frame.style.height).toBe("1076px");

    // Switching language triggers re-measurement even without a width change.
    naturalHeight = 1140;
    currentLang = "en";
    view.rerender(<FitStage stage={frame} narrow={true} />);
    expect(frame.style.height).toBe("1236px");

    // ResizeObserver callback also re-measures.
    naturalHeight = 1200;
    resizeCb?.([], {} as ResizeObserver);
    expect(frame.style.height).toBe("1296px");

    // Wide layout leaves the frame's height untouched.
    view.unmount();
    frame.style.height = "900px";
    naturalHeight = 1720;
    mount(<FitStage stage={frame} narrow={false} />);
    expect(frame.style.height).toBe("900px");
  });
});
