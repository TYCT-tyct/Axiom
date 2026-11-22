// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { brandFlight, pitchFlight } from "./fly";

// jsdom has no Web Animations: restoring a run's styles finishes what the revert started (style-tracker.ts settle)
Element.prototype.getAnimations = () => [];

afterEach(() => {
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

function domRect(left: number, top: number, width: number, height: number): DOMRect {
  return {
    x: left,
    y: top,
    left,
    top,
    right: left + width,
    bottom: top + height,
    width,
    height,
    toJSON: () => ({}),
  } as DOMRect;
}

function parseTranslate(el: HTMLElement): { x: number; y: number } {
  const m = /translate\(([-\d.]+)px,\s*([-\d.]+)px\)/.exec(el.style.getPropertyValue("transform"));
  return m ? { x: Number(m[1]), y: Number(m[2]) } : { x: 0, y: 0 };
}

// Simulates browser box layout for a flying copy positioned inside frame: translate shifts the box and
// padding-left/padding-top shift the first glyph inside it by the given intrinsic ink offset.
function attachCopyLayout(
  copy: HTMLElement,
  frame: { left: number; top: number },
  inkOffset: { x: number; y: number },
) {
  copy.getBoundingClientRect = () => {
    const tr = parseTranslate(copy);
    const w = Number.parseFloat(copy.style.getPropertyValue("width")) || 100;
    const h = Number.parseFloat(copy.style.getPropertyValue("line-height")) || 26;
    return domRect(frame.left + tr.x, frame.top + tr.y, w, h);
  };
  return () => {
    const tr = parseTranslate(copy);
    const pl = Number.parseFloat(copy.style.getPropertyValue("padding-left")) || 0;
    const pt = Number.parseFloat(copy.style.getPropertyValue("padding-top")) || 0;
    return domRect(frame.left + tr.x + pl + inkOffset.x, frame.top + tr.y + pt + inkOffset.y, 16, 32);
  };
}

it.each(["svg", "img"] as const)(
  "brandFlight lands %s copy exactly over source and destination and restores",
  (tag) => {
    const frame = document.createElement("div");
    frame.getBoundingClientRect = () => domRect(20, 10, 1200, 800);
    document.body.append(frame);

    const makeBrand = (box: DOMRect, markBox: DOMRect) => {
      const wrap = document.createElement("a");
      wrap.getBoundingClientRect = () => box;
      const mark =
        tag === "svg" ? document.createElementNS("http://www.w3.org/2000/svg", "svg") : document.createElement("img");
      mark.getBoundingClientRect = () => markBox;
      wrap.append(mark);
      frame.append(wrap);
      return { wrap, mark };
    };

    // Source mark sits 3px deeper inside its link than the copy's mark, destination mark sits 1.5px off.
    const from = makeBrand(domRect(68, 33, 96, 26), domRect(71, 36, 20, 20));
    const to = makeBrand(domRect(500, 410, 96, 26), domRect(501.5, 414, 20, 20));

    const copy = document.createElement("span");
    copy.style.setProperty("opacity", "0.9");
    const copyMark =
      tag === "svg" ? document.createElementNS("http://www.w3.org/2000/svg", "svg") : document.createElement("img");
    copyMark.getBoundingClientRect = () => {
      const tr = parseTranslate(copy);
      return domRect(20 + tr.x + 2, 10 + tr.y + 3, 20, 20);
    };
    copy.append(copyMark);
    frame.append(copy);

    const flight = brandFlight(copy, from.wrap, to.wrap);
    // Calibration must leave copy clean before go() runs.
    expect(copy.style.getPropertyValue("transform")).toBe("");
    expect(copy.style.getPropertyValue("transition")).toBe("");

    flight.go(0);
    expect(copy.style.getPropertyValue("visibility")).toBe("visible");
    expect(copyMark.getBoundingClientRect().left).toBeCloseTo(from.mark.getBoundingClientRect().left, 5);
    expect(copyMark.getBoundingClientRect().top).toBeCloseTo(from.mark.getBoundingClientRect().top, 5);

    flight.go(0.5);
    expect(copyMark.getBoundingClientRect().left).toBeCloseTo(
      (from.mark.getBoundingClientRect().left + to.mark.getBoundingClientRect().left) / 2,
      5,
    );

    flight.go(1);
    expect(copyMark.getBoundingClientRect().left).toBeCloseTo(to.mark.getBoundingClientRect().left, 5);
    expect(copyMark.getBoundingClientRect().top).toBeCloseTo(to.mark.getBoundingClientRect().top, 5);

    flight.restore();
    expect(copy.style.getPropertyValue("visibility")).toBe("");
    expect(copy.style.getPropertyValue("transform")).toBe("");
    expect(copy.style.getPropertyValue("transition")).toBe("");
    // Unrelated pre-existing inline style is preserved.
    expect(copy.style.getPropertyValue("opacity")).toBe("0.9");
  },
);

it("pitchFlight aligns headline and lede glyphs at both endpoints, interpolates lens vars, and restores", () => {
  const frameBox = { left: 10, top: 5 };
  const frame = document.createElement("div");
  frame.getBoundingClientRect = () => domRect(frameBox.left, frameBox.top, 1440, 900);
  document.body.append(frame);

  // Source hero headline (centered with .ch spans) and lede (with [data-dx]).
  const fromH = document.createElement("h1");
  fromH.style.fontSize = "80px";
  fromH.style.lineHeight = "84px";
  fromH.style.setProperty("--veil", "rgba(252, 252, 253, 0.50)");
  fromH.getBoundingClientRect = () => domRect(310.4, 240.6, 600, 84);
  const ch1 = document.createElement("span");
  ch1.className = "ch";
  ch1.textContent = "一";
  ch1.getBoundingClientRect = () => domRect(340.9, 244.2, 80, 76);
  const ch2 = document.createElement("span");
  ch2.className = "ch";
  ch2.textContent = "个";
  ch2.getBoundingClientRect = () => domRect(420.9, 244.2, 80, 76);
  fromH.append(ch1, ch2);

  const fromS = document.createElement("p");
  fromS.style.fontSize = "16px";
  fromS.style.lineHeight = "26px";
  fromS.getBoundingClientRect = () => domRect(410.2, 344.3, 400, 26);
  const fromSInner = document.createElement("span");
  fromSInner.dataset.dx = "r";
  fromSInner.textContent = "每条线路实测";
  fromSInner.getBoundingClientRect = () => domRect(430.7, 347.1, 360, 20);
  fromS.append(fromSInner);

  // Destination .lg-pitch headline and lede (both with .t wrappers).
  const toH = document.createElement("p");
  toH.style.fontSize = "40px";
  toH.style.lineHeight = "48px";
  toH.style.setProperty("--veil", "rgba(17, 17, 19, 0.45)");
  toH.getBoundingClientRect = () => domRect(58.3, 720.8, 320, 48);
  const toHInner = document.createElement("span");
  toHInner.className = "t";
  toHInner.textContent = "一个";
  toHInner.getBoundingClientRect = () => domRect(58.3, 723.5, 320, 42);
  toH.append(toHInner);

  const toS = document.createElement("p");
  toS.style.fontSize = "16px";
  toS.style.lineHeight = "26px";
  toS.getBoundingClientRect = () => domRect(58.3, 780.4, 300, 26);
  const toSInner = document.createElement("span");
  toSInner.className = "t";
  toSInner.textContent = "每条线路实测";
  toSInner.getBoundingClientRect = () => domRect(58.3, 783.0, 300, 20);
  toS.append(toSInner);

  // Flying copies (.fh and .fs) with non-zero intrinsic ink bearing offsets to prove Range calibration cancels them.
  const copyH = document.createElement("p");
  copyH.textContent = "一个";
  copyH.style.setProperty("--ly", "99px");
  const copyS = document.createElement("p");
  copyS.textContent = "每条线路实测";
  frame.append(fromH, fromS, toH, toS, copyH, copyS);

  const glyphOfCopyH = attachCopyLayout(copyH, frameBox, { x: 1.25, y: 2.5 });
  const glyphOfCopyS = attachCopyLayout(copyS, frameBox, { x: 0.75, y: 1.5 });

  // Mock Range.prototype.getBoundingClientRect to return each element's exact first-glyph rect.
  const glyphByOwner = new Map<Element, () => DOMRect>([
    [fromH, () => domRect(341.4, 245.1, 80, 76)],
    [toH, () => domRect(58.8, 724.2, 40, 42)],
    [fromS, () => domRect(431.1, 347.9, 16, 20)],
    [toS, () => domRect(58.6, 783.4, 16, 20)],
    [copyH, glyphOfCopyH],
    [copyS, glyphOfCopyS],
  ]);
  Range.prototype.getBoundingClientRect = function () {
    const container = this.startContainer;
    const owner = [...glyphByOwner.keys()].find((el) => el === container || el.contains(container));
    const read = owner ? glyphByOwner.get(owner) : undefined;
    return read ? read() : domRect(0, 0, 0, 0);
  };

  const flight = pitchFlight({ h: copyH, s: copyS }, { h: fromH, s: fromS }, { h: toH, s: toS });
  // Calibration restores everything immediately.
  expect(copyH.style.getPropertyValue("transform")).toBe("");
  expect(copyH.style.getPropertyValue("--ly")).toBe("99px");

  // u = 0: glyphs land exactly on source, transforms are integer pixels, fractional part lives in padding.
  flight.go(0, 0, 0, 0);
  expect(glyphOfCopyH().left).toBeCloseTo(341.4, 5);
  expect(glyphOfCopyH().top).toBeCloseTo(245.1, 5);
  expect(glyphOfCopyS().left).toBeCloseTo(431.1, 5);
  expect(glyphOfCopyS().top).toBeCloseTo(347.9, 5);
  expect(Number.isInteger(parseTranslate(copyH).x)).toBe(true);
  expect(Number.isInteger(parseTranslate(copyH).y)).toBe(true);
  expect(copyH.style.getPropertyValue("font-size")).toBe("80px");
  expect(copyH.style.getPropertyValue("line-height")).toBe("84px");
  expect(copyH.style.getPropertyValue("--ly")).toBe("16px");
  expect(copyH.style.getPropertyValue("--lx")).toBe("32px");
  expect(copyH.style.getPropertyValue("--mx")).toBe("24px");
  expect(copyH.style.getPropertyValue("--my")).toBe("24px");
  expect(copyH.style.getPropertyValue("--fv")).toBe("rgba(252,252,253,0.500)");

  // Mid-flight with distinct ux/uy/us/uxl: lede trails horizontally on uxl while staying locked beneath headline.
  flight.go(0.5, 0.25, 0.5, 0.2);
  expect(glyphOfCopyH().left).toBeCloseTo((341.4 + 58.8) / 2, 5);
  expect(glyphOfCopyH().top).toBeCloseTo(245.1 + (724.2 - 245.1) * 0.25, 5);
  expect(glyphOfCopyS().left).toBeCloseTo(431.1 + (58.6 - 431.1) * 0.2, 5);
  expect(copyH.style.getPropertyValue("font-size")).toBe("60px");
  expect(copyH.style.getPropertyValue("--ly")).toBe("14px");
  expect(copyH.style.getPropertyValue("--lx")).toBe("28px");
  expect(copyH.style.getPropertyValue("--mx")).toBe("22px");
  expect(copyH.style.getPropertyValue("--my")).toBe("20px");
  expect(copyH.style.getPropertyValue("--fv")).toBe("rgba(135,135,136,0.475)");

  // u = 1: glyphs land exactly on destination with destination metrics and lens vars.
  flight.go(1, 1, 1, 1);
  expect(glyphOfCopyH().left).toBeCloseTo(58.8, 5);
  expect(glyphOfCopyH().top).toBeCloseTo(724.2, 5);
  expect(glyphOfCopyS().left).toBeCloseTo(58.6, 5);
  expect(glyphOfCopyS().top).toBeCloseTo(783.4, 5);
  expect(copyH.style.getPropertyValue("font-size")).toBe("40px");
  expect(copyH.style.getPropertyValue("line-height")).toBe("48px");
  expect(copyH.style.getPropertyValue("width")).toBe("320px");
  expect(copyH.style.getPropertyValue("--ly")).toBe("12px");
  expect(copyH.style.getPropertyValue("--lx")).toBe("24px");
  expect(copyH.style.getPropertyValue("--mx")).toBe("20px");
  expect(copyH.style.getPropertyValue("--my")).toBe("16px");
  expect(copyH.style.getPropertyValue("--fv")).toBe("rgba(17,17,19,0.450)");

  // restore puts back pre-existing values and clears every property set during flight.
  flight.restore();
  expect(copyH.style.getPropertyValue("--ly")).toBe("99px");
  for (const prop of [
    "visibility",
    "transform",
    "width",
    "padding-left",
    "padding-top",
    "font-size",
    "line-height",
    "transition",
    "--lx",
    "--mx",
    "--my",
    "--fv",
  ]) {
    expect(copyH.style.getPropertyValue(prop)).toBe("");
    expect(copyS.style.getPropertyValue(prop)).toBe("");
  }
  expect(copyS.style.getPropertyValue("--ly")).toBe("");
});

it("pitchFlight fades a lede hidden at the destination while riding under the headline, and restores opacity", () => {
  const frameBox = { left: 0, top: 0 };
  const frame = document.createElement("div");
  frame.getBoundingClientRect = () => domRect(0, 0, 390, 844);
  document.body.append(frame);

  const fromH = document.createElement("h1");
  fromH.style.fontSize = "40px";
  fromH.style.lineHeight = "44px";
  fromH.textContent = "标题";
  fromH.getBoundingClientRect = () => domRect(20, 300, 350, 44);

  const fromS = document.createElement("p");
  fromS.style.fontSize = "16px";
  fromS.style.lineHeight = "26px";
  fromS.textContent = "副标题";
  fromS.getBoundingClientRect = () => domRect(20, 360, 350, 26);

  const toH = document.createElement("p");
  toH.style.fontSize = "28px";
  toH.style.lineHeight = "36px";
  toH.textContent = "标题";
  toH.getBoundingClientRect = () => domRect(20, 152, 350, 36);

  // Phone: .lg-pitch .s is display:none -> zero-width rect.
  const toS = document.createElement("p");
  toS.textContent = "副标题";
  toS.getBoundingClientRect = () => domRect(0, 0, 0, 0);

  const copyH = document.createElement("p");
  copyH.textContent = "标题";
  const copyS = document.createElement("p");
  copyS.textContent = "副标题";
  frame.append(fromH, fromS, toH, toS, copyH, copyS);

  const glyphH = attachCopyLayout(copyH, frameBox, { x: 0, y: 0 });
  const glyphS = attachCopyLayout(copyS, frameBox, { x: 0, y: 0 });
  Range.prototype.getBoundingClientRect = function () {
    const c = this.startContainer;
    if (copyH.contains(c)) return glyphH();
    if (copyS.contains(c)) return glyphS();
    if (fromH.contains(c)) return fromH.getBoundingClientRect();
    if (fromS.contains(c)) return fromS.getBoundingClientRect();
    if (toH.contains(c)) return toH.getBoundingClientRect();
    return domRect(0, 0, 0, 0);
  };

  const flight = pitchFlight({ h: copyH, s: copyS }, { h: fromH, s: fromS }, { h: toH, s: toS });
  flight.go(0, 0, 0, 0);
  expect(copyS.style.getPropertyValue("opacity")).toBe("1");
  expect(glyphS().top).toBeCloseTo(360, 5);

  flight.go(0.5, 0.5, 0.5, 0.5);
  expect(Number(copyS.style.getPropertyValue("opacity"))).toBeCloseTo(0.2, 5);

  flight.go(1, 1, 1, 1);
  expect(copyS.style.getPropertyValue("opacity")).toBe("0");
  // Rides 8px below destination headline (152 + 36 + 8 = 196) and keeps source width/font size.
  expect(glyphS().left).toBeCloseTo(20, 5);
  expect(glyphS().top).toBeCloseTo(196, 5);
  expect(copyS.style.getPropertyValue("width")).toBe("350px");

  flight.restore();
  expect(copyS.style.getPropertyValue("opacity")).toBe("");
});
