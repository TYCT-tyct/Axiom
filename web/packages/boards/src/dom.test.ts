// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import { boardCSS, boardHTML, boardStyle } from "./dom.ts";

it("keeps style attributes out of the parser, including inert templates", () => {
  const parse = DOMParser.prototype.parseFromString;
  const activeStyles: string[] = [];
  const spy = vi.spyOn(DOMParser.prototype, "parseFromString").mockImplementation(function (this: DOMParser, ...args) {
    const doc = parse.apply(this, args);
    // Capture at parse time: removing an attribute later cannot undo a CSP violation already reported by the parser.
    const template = doc.querySelector("template");
    for (const node of (template?.content ?? doc).querySelectorAll("[style]"))
      activeStyles.push(node.getAttribute("style") ?? "");
    return doc;
  });
  try {
    const target = document.createElement("div");
    boardHTML(target, '<span style="--w: var(--w0);"><b style="font-weight:400">A</b></span>');
    expect(activeStyles).toEqual([]);
    expect(target.querySelector("span")?.style.getPropertyValue("--w")).toBe("var(--w0)");
    expect(target.querySelector("b")?.style.fontWeight).toBe("400");
  } finally {
    spy.mockRestore();
  }
});

it("preserves quoted content and style declarations through inert HTML parsing", () => {
  const el = document.createElement("div");
  boardHTML(el, `<span title='a > style="unchanged"' STYLE = 'color:red; --text:"a;b"'>&lt;style=hello&gt;</span>`);
  const span = el.firstElementChild as HTMLElement;
  expect(span.title).toBe('a > style="unchanged"');
  expect(span.style.color).toBe("red");
  expect(span.style.getPropertyValue("--text")).toBe('"a;b"');
  expect(span.textContent).toBe("<style=hello>");
  expect(span.hasAttribute("data-board-style")).toBe(false);
});

it("passes drawing-facade writes through their converted setter to every SVG copy", () => {
  const copies = [0, 1].map(() => document.createElementNS("http://www.w3.org/2000/svg", "g"));
  const drawing = {
    set innerHTML(html: string) {
      for (const copy of copies) boardHTML(copy, html);
    },
  };
  boardHTML(drawing, '<path d="M0 0L1 1" style="stroke:red"/>');
  for (const copy of copies) {
    expect(copy.firstElementChild?.namespaceURI).toBe(copy.namespaceURI);
    expect((copy.firstElementChild as SVGElement).style.stroke).toBe("red");
  }
});

it("keeps variable shorthands rather than copying their empty pending-substitution longhands", () => {
  const sheet = {
    cssRules: [
      {
        style: {
          length: 1,
          0: "background-color",
          getPropertyValue: (name: string) => (name === "background" ? "var(--c0)" : ""),
          getPropertyPriority: () => "",
        },
      },
    ],
    insertRule: () => 0,
  };
  vi.stubGlobal(
    "CSSStyleSheet",
    class {
      cssRules = sheet.cssRules;
      insertRule = sheet.insertRule;
    },
  );
  try {
    expect(boardStyle("background:var(--c0)")).toEqual({ background: "var(--c0)" });
    const style = document.createElement("i").style;
    boardCSS(style, "background:var(--c0)");
    expect(style.background).toBe("var(--c0)");
  } finally {
    vi.unstubAllGlobals();
  }
});

it("preserves table and SVG structure, text and styles without assigning style attributes", () => {
  const row = document.createElement("tr");
  boardHTML(row, '<td><span style="color: red; --n: 2">&lt;hello&gt;</span></td>');
  expect(row.cells).toHaveLength(1);
  expect(row.textContent).toBe("<hello>");
  expect(row.querySelector("span")?.style.color).toBe("red");
  expect(row.querySelector("span")?.style.getPropertyValue("--n")).toBe("2");
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  boardHTML(svg, '<path d="M0 0" style="fill: red" />');
  expect(svg.firstElementChild?.namespaceURI).toBe(svg.namespaceURI);
  expect(boardStyle("width: 12px; --n: 3")).toEqual({ width: "12px", "--n": "3" });
  const el = document.createElement("div");
  boardCSS(el.style, "width:1px; color:red");
  boardCSS(el.style, "color:blue", true);
  expect(el.style.width).toBe("1px");
  boardCSS(el.style, "height:2px");
  expect(el.style.width).toBe("");
});

it("parses each style text once and shares the frozen result", () => {
  const parse = vi.spyOn(CSSStyleSheet.prototype, "insertRule");
  try {
    const first = boardStyle("--once: 1; width: 3px");
    expect(boardStyle("--once: 1; width: 3px")).toBe(first);
    expect(parse).toHaveBeenCalledTimes(1);
    expect(first).toEqual({ "--once": "1", width: "3px" });
    expect(Object.isFrozen(first)).toBe(true);
  } finally {
    parse.mockRestore();
  }
});
