// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { startSea } from "./sea";

type Callback = (entries: IntersectionObserverEntry[]) => void;

const ctxStub = () =>
  ({
    setTransform: vi.fn(),
    clearRect: vi.fn(),
    fillRect: vi.fn(),
    fillText: vi.fn(),
    beginPath: vi.fn(),
    arc: vi.fn(),
    fill: vi.fn(),
    measureText: vi.fn(() => ({ width: 8 })),
  }) as unknown as CanvasRenderingContext2D;

function buildFrame() {
  const root = document.createElement("div");
  root.className = "tr site d light";
  const sea = document.createElement("canvas");
  sea.className = "sea";
  const cover = document.createElement("canvas");
  cover.className = "sea cover";
  const ho = document.createElement("div");
  ho.className = "ho light";
  const brand = document.createElement("a");
  brand.className = "brand";
  const main = document.createElement("main");
  const h1 = document.createElement("h1");
  const ch1 = document.createElement("span");
  ch1.className = "ch";
  ch1.textContent = "一";
  const ch2 = document.createElement("span");
  ch2.className = "ch";
  ch2.textContent = "个";
  h1.append(ch1, ch2);
  const lede = document.createElement("span");
  lede.dataset.dx = "r";
  lede.dataset.at = "40";
  lede.dataset.per = "20";
  lede.textContent = "中文说明";
  const scramble = document.createElement("canvas");
  scramble.className = "scramble";
  main.append(h1, lede);
  ho.append(brand, main, scramble);
  root.append(sea, cover, ho);
  document.body.append(root);
  for (const canvas of [sea, cover, scramble]) {
    Object.defineProperty(canvas, "clientWidth", { value: 800, configurable: true });
    Object.defineProperty(canvas, "clientHeight", { value: 600, configurable: true });
    canvas.getContext = vi.fn(ctxStub) as unknown as typeof canvas.getContext;
  }
  return { root, owner: main, brand, lede, chars: [ch1, ch2] };
}

describe("startSea entrance selection", () => {
  const rafs = new Map<number, FrameRequestCallback>();
  let nextId = 1;
  let seen: Callback | undefined;

  const step = (now: number) => {
    const batch = [...rafs.entries()];
    rafs.clear();
    for (const [, fn] of batch) fn(now);
  };

  // the engine the last startSea built
  let built: ReturnType<typeof makeEngine> | undefined;
  const makeEngine = () => {
    const engine = {
      canvas: document.createElement("canvas"),
      renderer: {
        cw: 10,
        ch: 16,
        cols: 10,
        rows: 10,
        bg: "#030302",
        font: "monospace",
        energy: new Float32Array(100),
        energyTmp: new Float32Array(100),
        heat: new Float32Array(100),
      },
      vs: 1,
      held: true,
      world: { width: 800, height: 600, time: 0, sharks: [], life: { st: "hidden", t: 0 } },
      hold(v: boolean) {
        this.held = v;
      },
      still: vi.fn(),
      schedule: vi.fn(),
      tick: vi.fn(),
      setLight: vi.fn(),
      destroy: vi.fn(),
    };
    return engine;
  };

  beforeEach(() => {
    document.body.innerHTML = "";
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
        constructor(callback: Callback) {
          seen = callback;
        }
        observe(target: Element) {
          seen?.([{ target, isIntersecting: true } as IntersectionObserverEntry]);
        }
        unobserve() {}
        disconnect() {}
      },
    );
    window.AxiomOceanOrig = {
      Engine: class {
        constructor() {
          built = Object.assign(this, makeEngine());
        }
      } as unknown as typeof window.AxiomOceanOrig.Engine,
    };
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
  });

  it("picks tide when Math.random() < 0.5 and completes on the shared loop", () => {
    vi.spyOn(Math, "random").mockReturnValue(0.1);
    const { root, owner } = buildFrame();
    const sea = startSea(root, owner, true);
    expect(root.dataset.enter).toBe("tide");
    step(0);
    for (let t = 100; t <= 1300; t += 100) step(t);
    expect(root.dataset.enter).toBe("done");
    sea.destroy();
  });

  it("keeps a home page settling back from sign-in settled: no entrance plays, and the sea moves", () => {
    vi.spyOn(Math, "random").mockReturnValue(0.9);
    const { root, owner, chars } = buildFrame();
    root.dataset.enter = "done";
    const sea = startSea(root, owner, true);
    expect(root.dataset.enter).toBe("done");
    for (let t = 0; t <= 1800; t += 100) step(t);
    expect(root.dataset.enter).toBe("done");
    expect(chars[0]?.style.color).toBe("");
    // with no entrance to let it go, the sea is let go at once: it moves from the first frame
    expect(built?.held).toBe(false);
    expect(built?.tick).toHaveBeenCalled();
    sea.destroy();
  });

  it("picks decode when Math.random() >= 0.5, decodes cells, and restores text on completion", () => {
    vi.spyOn(Math, "random").mockReturnValue(0.9);
    const { root, owner, lede, chars } = buildFrame();
    const sea = startSea(root, owner, true);
    expect(root.dataset.enter).toBe("decode");
    // mid-decode: undecoded title characters are transparent and slot text is mid-animation
    step(0);
    step(50);
    expect(chars[0]?.style.color).toBe("transparent");
    for (let t = 150; t <= 1800; t += 100) step(t);
    expect(root.dataset.enter).toBe("done");
    expect(chars[0]?.style.color).toBe("");
    expect(lede.textContent).toBe("中文说明");
    sea.destroy();
  });

  it("settles an active decode entrance immediately and replays in the current language on logo click", () => {
    vi.spyOn(Math, "random").mockReturnValue(0.9);
    const { root, owner, brand, lede } = buildFrame();
    const sea = startSea(root, owner, true);
    expect(root.dataset.enter).toBe("decode");
    sea.settle?.();
    expect(root.dataset.enter).toBe("done");
    expect(lede.textContent).toBe("中文说明");

    // Simulate prefs.js rewriting the text into English, then clicking the logo to replay.
    lede.textContent = "English lede";
    brand.click();
    expect(root.dataset.enter).toBe("decode");
    sea.settle?.();
    expect(root.dataset.enter).toBe("done");
    expect(lede.textContent).toBe("English lede");
    sea.destroy();
  });
});
