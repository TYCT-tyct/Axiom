import { cpSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { boardNames, engineNames } from "../packages/boards/catalog.ts";
import { readBoard } from "../packages/boards/source.ts";
import { csp, exportComponent } from "./boards/csp.ts";
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

  it("decodes entity-encoded props and exports only the top-level component in composite boards", () => {
    const nested =
      "const child = (() => { class Component extends DCLogic {} return Component; })();\nclass Component extends DCLogic {}";
    expect(exportComponent(nested)).toBe(nested.replace("\nclass Component", "\nexport class Component"));
    const encoded = sample
      .replace("data-props='", 'data-props="')
      .replace(/'>(class Component)/, '">$1')
      .replaceAll('"mode"', "&quot;mode&quot;")
      .replaceAll('"options"', "&quot;options&quot;")
      .replaceAll('"default"', "&quot;default&quot;")
      .replaceAll('"light"', "&quot;light&quot;")
      .replaceAll('"dark"', "&quot;dark&quot;");
    expect(convert(encoded, "Test")["Test/Board.tsx"]).toContain('"mode"?: "light" | "dark"');
  });

  it.skipIf(!process.env.AXIOM_DESIGN_DIR)("pins all source board and engine bytes without executing them", () => {
    const directory = process.env.AXIOM_DESIGN_DIR as string;
    const design = existsSync(join(directory, "project")) ? join(directory, "project") : directory;
    for (const name of boardNames)
      expect(readBoard(join(packageRoot, "inputs", `${name}.dc.html`)), name).toBe(
        readFileSync(join(design, `${name}.dc.html`), "utf8"),
      );
    for (const name of engineNames)
      expect(readFileSync(join(packageRoot, "inputs", `${name}.js.txt`), "utf8"), name).toBe(
        readFileSync(join(design, `${name}.js`), "utf8"),
      );
  });

  // AxApp is a 1 MiB artboard; these integration checks parse its complete DOM.
  it("CI regenerates every pinned board and engine without a diff", () => {
    expect(checkGenerated()).toEqual([]);
  }, 60_000);

  it("detects changed inputs and missing English keys, and leaves handwritten wrappers alone", () => {
    const root = mkdtempSync(join(tmpdir(), "axiom-boards-"));
    try {
      cpSync(join(packageRoot, "inputs"), join(root, "inputs"), { recursive: true });
      cpSync(join(packageRoot, "src"), join(root, "src"), { recursive: true });
      const file = join(root, "src/Login/board.css");
      const english = join(root, "src/AxApp/regions/Overview.en.ts");
      const input = join(root, "inputs/Login.head.html");
      writeFileSync(
        input,
        readFileSync(input, "utf8").replace("</helmet>", "<style>.source-change{color:red}</style></helmet>"),
      );
      writeFileSync(english, readFileSync(english, "utf8").replace(/^ {2}"m[\da-f]+": .*\n/m, ""));
      writeFileSync(join(root, "src/wrapper.tsx"), "export const wrapper = 1;");
      expect(checkGenerated(root)).toEqual(["Login/board.css", "AxApp/regions/Overview.en.ts"]);
      generate(root);
      expect(readFileSync(file, "utf8")).toBe(
        convert(readBoard(join(root, "inputs/Login.dc.html")), "Login")["Login/board.css"],
      );
      expect(readFileSync(english, "utf8")).toBe(
        readFileSync(join(packageRoot, "src/AxApp/regions/Overview.en.ts"), "utf8"),
      );
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
