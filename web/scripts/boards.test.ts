import { cpSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { csp } from "./boards/csp.ts";
import { checkGenerated, convert, generate, generatedBoard, marker, packageRoot } from "./boards.ts";

const sample = `<x-dc><helmet><style>@font-face{font-family:x;src:url(x)}.test { color: red; }</style></helmet><div class="test {{ cls }}" ref="{{ root }}" style="width: {{ width }}px; {{ vars }}"><svg viewBox="0 0 10 10"><path stroke-width="2" /></svg><button onClick="{{ click }}">{{ text }}</button><input value="demo" /></div></x-dc><script data-dc-script data-props='{"mode":{"options":["light","dark"],"default":"light"}}'>class Component extends DCLogic { renderVals() { return {}; } }</script>`;

describe("board conversion", () => {
  it("preserves CSS and logic, types defaults, and converts holes, SVG, handlers and styles", () => {
    const files = convert(sample, "Test");
    expect(files["Test/board.css"]).toContain(".test { color: red; }");
    expect(files["Test/board.css"]).not.toContain("@font-face");
    expect(files["Test/logic.ts"]).toContain("renderVals() { return {}; }");
    const jsx = files["Test/Board.tsx"];
    for (const expected of [
      '"mode"?: "light" | "dark"',
      '"mode":"light"',
      "className=",
      "boardStyle(",
      "v.width",
      "v.vars",
      'viewBox={"0 0 10 10"}',
      'strokeWidth={"2"}',
      "onClick={v.click}",
      'defaultValue={"demo"}',
    ])
      expect(jsx).toContain(expected);
    expect(convert(sample.replace("color: red", "color: blue"), "Test")["Test/board.css"]).toContain("color: blue");
  });

  it("mechanically adapts CSP writes while leaving other script bytes alone", () => {
    expect(csp("const x = 1; el.innerHTML = '<i style=\"color:red\">hi</i>'; el.style.cssText += 'width:1px';")).toBe(
      `const x = 1; boardHTML(el, '<i style="color:red">hi</i>', "inner"); boardCSS(el.style, 'width:1px', true);`,
    );
  });

  // AxApp is a 1 MiB artboard; these integration checks parse its complete DOM.
  it("CI regenerates every pinned board and engine without a diff", () => {
    expect(checkGenerated()).toEqual([]);
  }, 60_000);

  it("detects edited generated files and leaves handwritten wrappers alone", () => {
    const root = mkdtempSync(join(tmpdir(), "axiom-boards-"));
    try {
      cpSync(join(packageRoot, "inputs"), join(root, "inputs"), { recursive: true });
      cpSync(join(packageRoot, "src"), join(root, "src"), { recursive: true });
      const file = join(root, "src/Login/board.css");
      writeFileSync(file, `${readFileSync(file, "utf8")}\n.bad{color:red}`);
      writeFileSync(join(root, "src/wrapper.tsx"), "export const wrapper = 1;");
      expect(checkGenerated(root)).toEqual(["Login/board.css"]);
      generate(root);
      expect(readFileSync(file, "utf8")).toBe(readFileSync(join(packageRoot, "src/Login/board.css"), "utf8"));
      expect(readFileSync(join(root, "src/wrapper.tsx"), "utf8")).toBe("export const wrapper = 1;");
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  }, 60_000);

  it("only recognizes the marker inside the generated board tree", () => {
    expect(generatedBoard("packages/boards/src/Login/Board.tsx", `${marker}\n`)).toBe(true);
    expect(generatedBoard("console/src/page.tsx", `${marker}\n`)).toBe(false);
    expect(generatedBoard("packages/boards/src/runtime.tsx", "export class Runtime {}")).toBe(false);
  });
});
