// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { pen, restore, slotsOf } from "./pen";
import { GLYPHS } from "./word-tiles";

afterEach(() => {
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

function rect(left: number, top: number, width: number, height: number): DOMRect {
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

function makeWord(text: string, mode: "r" | "s", box?: DOMRect): HTMLSpanElement {
  const el = document.createElement("span");
  el.dataset.dx = mode;
  el.textContent = text;
  if (box) el.getBoundingClientRect = () => box;
  return el;
}

function partsOf(el: HTMLElement) {
  const [head, ...spans] = [...el.childNodes];
  return {
    head: head?.textContent ?? "",
    spans: spans.map((n) => ({ text: n.textContent ?? "", color: (n as HTMLElement).style.color })),
  };
}

it("collects [data-dx] slots, applies skip, and measures centre distance from the trigger point", () => {
  const scope = document.createElement("div");
  const a = makeWord("模型", "r", rect(90, 40, 20, 20)); // centre (100, 50)
  const b = makeWord("claude", "s", rect(390, 440, 20, 20)); // centre (400, 450)
  const skipped = makeWord("跳过", "r", rect(0, 0, 10, 10));
  scope.append(a, b, skipped);
  document.body.append(scope);

  const slots = slotsOf(scope, { x: 100, y: 50 }, (el) => el === skipped);
  expect(slots).toHaveLength(2);
  expect(slots[0]).toEqual({ el: a, text: "模型", mode: "r", d: 0 });
  expect(slots[1]).toEqual({ el: b, text: "claude", mode: "s", d: 500 });

  const noOrigin = slotsOf(scope);
  expect(noOrigin.map((s) => s.d)).toEqual([0, 0, 0]);
});

it.each([
  { written: 0, head: "", ring: "", rest: "注册，获取" },
  { written: 0.2, head: "", ring: "注", rest: "册，获取" },
  { written: 0.5, head: "注册", ring: "，", rest: "获取" },
  { written: 0.8, head: "注册，", ring: "获", rest: "取" },
])(
  "mode r splits visible prefix, ring tip and transparent tail at written=$written",
  ({ written, head, ring, rest }) => {
    const scope = document.createElement("div");
    const word = makeWord("注册，获取", "r");
    scope.append(word);
    document.body.append(scope);
    const [slot] = slotsOf(scope);
    if (!slot) throw new Error("expected slot");

    pen(slot, written, 0);
    expect(partsOf(word)).toEqual({
      head,
      spans: [
        { text: ring, color: "var(--ring)" },
        { text: rest, color: "transparent" },
      ],
    });
    expect(word.textContent).toBe("注册，获取");
    expect(word.getAttribute("aria-label")).toBe("注册，获取");

    pen(slot, 1, 0);
    expect(word.childNodes).toHaveLength(1);
    expect(word.textContent).toBe("注册，获取");
    expect(word.hasAttribute("aria-label")).toBe(false);
  },
);

it("mode s runs cycling mono glyphs ahead of the settled prefix while preserving spaces and width", () => {
  const scope = document.createElement("div");
  const word = makeWord("a bcd ef", "s");
  scope.append(word);
  document.body.append(scope);
  const [slot] = slotsOf(scope);
  if (!slot) throw new Error("expected slot");

  pen(slot, 0, 1);
  const p0 = partsOf(word);
  expect(p0.head).toBe("");
  expect(p0.spans).toHaveLength(3);
  expect(p0.spans.map((s) => s.color)).toEqual(["var(--ring)", "var(--text2)", "transparent"]);
  expect(GLYPHS).toContain(p0.spans[0]?.text);
  // index 1 is a space and must remain a space so word spacing never shifts
  expect(p0.spans[1]?.text[0]).toBe(" ");
  expect(p0.spans[1]?.text).toHaveLength(3);
  expect(p0.spans[2]?.text).toBe("d ef");
  expect([...(word.textContent ?? "")].length).toBe(8);

  pen(slot, 0.5, 2);
  const p5 = partsOf(word);
  expect(p5.head).toBe("a bc");
  expect(GLYPHS).toContain(p5.spans[0]?.text);
  expect(p5.spans[1]?.text[0]).toBe(" ");
  expect(p5.spans[1]?.text).toHaveLength(3);
  expect(p5.spans[2]?.text).toBe("");

  const snapshot = word.textContent;
  pen(slot, 0.5, 3);
  expect(word.textContent).not.toBe(snapshot);

  pen(slot, 1, 3);
  expect(word.textContent).toBe("a bcd ef");
  expect(word.querySelectorAll("span")).toHaveLength(0);
});

it("skips DOM writes on repeated calls and reuses the same span nodes across updates", () => {
  const scope = document.createElement("div");
  const rWord = makeWord("登录", "r");
  const sWord = makeWord("claude", "s");
  scope.append(rWord, sWord);
  document.body.append(scope);
  const [rSlot, sSlot] = slotsOf(scope);
  if (!rSlot || !sSlot) throw new Error("expected slots");

  const replaceSpy = vi.spyOn(Element.prototype, "replaceChildren");
  const attrSpy = vi.spyOn(Element.prototype, "setAttribute");
  const textSetter = vi.spyOn(Node.prototype, "textContent", "set");
  const dataSetter = vi.spyOn(CharacterData.prototype, "data", "set");

  // Full text on an untouched slot writes nothing.
  pen(rSlot, 1, 0);
  expect(replaceSpy).not.toHaveBeenCalled();
  expect(attrSpy).not.toHaveBeenCalled();
  expect(textSetter).not.toHaveBeenCalled();
  expect(dataSetter).not.toHaveBeenCalled();

  // First partial call builds nodes once; second call with the same state writes nothing (even across ticks for r).
  pen(rSlot, 0.5, 1);
  expect(replaceSpy).toHaveBeenCalledTimes(1);
  expect(textSetter).toHaveBeenCalled();
  const firstSpans = [...rWord.querySelectorAll("span")];
  pen(sSlot, 0.5, 4);
  replaceSpy.mockClear();
  attrSpy.mockClear();
  textSetter.mockClear();
  dataSetter.mockClear();

  pen(rSlot, 0.5, 99);
  pen(sSlot, 0.5, 4);
  expect(replaceSpy).not.toHaveBeenCalled();
  expect(attrSpy).not.toHaveBeenCalled();
  expect(textSetter).not.toHaveBeenCalled();
  expect(dataSetter).not.toHaveBeenCalled();

  // Advancing written updates text without replacing children and reuses the exact same span instances.
  pen(rSlot, 0, 99);
  expect(replaceSpy).not.toHaveBeenCalled();
  expect(textSetter).toHaveBeenCalled();
  expect([...rWord.querySelectorAll("span")]).toEqual(firstSpans);
});

it("hides underlined link decoration while penned and restores text, colours and aria-label cleanly", () => {
  const scope = document.createElement("div");
  const plainLink = document.createElement("a");
  plainLink.style.textDecorationLine = "underline";
  const w1 = makeWord("联系我们", "r");
  plainLink.append(w1);

  const styledLink = document.createElement("a");
  styledLink.style.textDecorationLine = "underline";
  styledLink.style.textDecorationColor = "rgb(10, 20, 30)";
  const w2 = makeWord("文档", "r");
  w2.setAttribute("aria-label", "阅读文档");
  styledLink.append(w2);

  const noUnderline = document.createElement("a");
  noUnderline.style.textDecorationLine = "none";
  const w3 = makeWord("登录", "r");
  noUnderline.append(w3);

  scope.append(plainLink, styledLink, noUnderline);
  document.body.append(scope);

  const slots = slotsOf(scope);
  for (const s of slots) pen(s, 0.5, 0);

  expect(plainLink.style.getPropertyValue("text-decoration-color")).toBe("transparent");
  expect(styledLink.style.getPropertyValue("text-decoration-color")).toBe("transparent");
  expect(noUnderline.style.getPropertyValue("text-decoration-color")).toBe("");
  expect(w1.getAttribute("aria-label")).toBe("联系我们");
  // Existing aria-label must not be overwritten.
  expect(w2.getAttribute("aria-label")).toBe("阅读文档");

  restore(slots);
  expect(w1.textContent).toBe("联系我们");
  expect(w1.querySelectorAll("span")).toHaveLength(0);
  expect(w1.hasAttribute("aria-label")).toBe(false);
  expect(plainLink.style.getPropertyValue("text-decoration-color")).toBe("");

  expect(w2.textContent).toBe("文档");
  expect(w2.getAttribute("aria-label")).toBe("阅读文档");
  expect(styledLink.style.getPropertyValue("text-decoration-color")).toBe("rgb(10, 20, 30)");

  // Re-penning after restore works (mid-flight reversal).
  const first = slots[0];
  if (!first) throw new Error("expected first slot");
  pen(first, 0.25, 0);
  expect(w1.querySelectorAll("span")).toHaveLength(2);
  expect(plainLink.style.getPropertyValue("text-decoration-color")).toBe("transparent");
});
