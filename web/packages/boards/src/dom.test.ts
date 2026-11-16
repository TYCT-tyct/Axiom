// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import { boardCSS, boardHTML, boardStyle } from "./dom.ts";

it("keeps style attributes out of the parsed document's active tree", () => {
  const parse = DOMParser.prototype.parseFromString;
  const activeStyles: string[] = [];
  const spy = vi.spyOn(DOMParser.prototype, "parseFromString").mockImplementation(function (this: DOMParser, ...args) {
    const doc = parse.apply(this, args);
    // Capture at parse time: removing an attribute later cannot undo a CSP violation already reported by the parser.
    for (const node of doc.querySelectorAll("[style]")) activeStyles.push(node.getAttribute("style") ?? "");
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
