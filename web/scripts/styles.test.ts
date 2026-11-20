import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { type Baseline, baselinePath, check, tree } from "./styles.ts";

const tokens = readFileSync(join(import.meta.dirname, "../packages/ui/src/css/tokens.css"), "utf8");
const none: Baseline = { migrated: [], counts: {} };
const component = (
  css: string,
  tsx = `import s from "./box.module.css";\nexport const Box = () => <div className={s.root} />;\n`,
) => ({
  "packages/ui/src/css/tokens.css": tokens,
  "packages/ui/src/components/box.module.css": css,
  "packages/ui/src/components/box.tsx": tsx,
});
const run = (files: Record<string, string>, baseline = none) =>
  check({ "packages/ui/src/css/tokens.css": tokens, ...files }, baseline);
const page = (body: string) => ({ "console/src/pages/a.tsx": `export const A = () => (\n  ${body}\n);\n` });

describe("app code", () => {
  it.each([
    { name: "a className", body: '<Card className="p-4" />', want: "console/src/pages/a.tsx:2: class-name" },
    { name: "an inline style", body: "<Card style={{ color: x }} />", want: "console/src/pages/a.tsx:2: style" },
    { name: "a CSS import", body: '<p />;\nimport "./a.css"', want: "console/src/pages/a.tsx:3: css-import" },
  ])("fails on $name, naming the file and line", ({ body, want }) => {
    expect(run(page(body)).join("\n")).toContain(want);
  });

  it("passes custom properties and typed props", () => {
    expect(run(page('<Card layout="flush" style={{ "--i": 2 }} />'))).toEqual([]);
  });

  it("allows the generated AxApp stylesheet only in the console and admin shells", () => {
    const sheet = 'import "@axiom/boards/AxApp/regions.css";\nexport const L = () => <div />;\n';
    expect(run({ "console/src/layout.tsx": sheet, "admin/src/layout.tsx": sheet })).toEqual([]);
    expect(run({ "console/src/pages/a.tsx": sheet }).join("\n")).toContain("console/src/pages/a.tsx:1: css-import");
    expect(run({ "console/src/layout.tsx": 'import "./extra.css";\n' }).join("\n")).toContain(
      "console/src/layout.tsx:1: css-import",
    );
  });

  it("keeps a baselined use and refuses one more", () => {
    const baseline: Baseline = { migrated: [], counts: { "class-name": { "console/src/pages/a.tsx": 1 } } };
    expect(run(page('<Card className="p-4" />'), baseline)).toEqual([]);
    expect(run(page('<><Card className="p-4" />\n<Card className="p-2" /></>'), baseline)).toEqual([
      expect.stringMatching(/^console\/src\/pages\/a\.tsx:3: class-name: .*2 here, 1 allowed \(lines 2, 3\)$/),
    ]);
  });

  it("asks for the baseline to shrink when a use goes", () => {
    const baseline: Baseline = { migrated: [], counts: { "class-name": { "console/src/pages/a.tsx": 2 } } };
    expect(run(page('<Card className="p-4" />'), baseline)).toEqual([
      "console/src/pages/a.tsx: class-name: 1 left of 2; lower it in scripts/style-baseline.json (--shrink)",
    ]);
  });

  it("allows nothing in a migrated module, whatever the counts say", () => {
    const baseline: Baseline = {
      migrated: ["console/src/pages/"],
      counts: { "class-name": { "console/src/pages/a.tsx": 1 } },
    };
    expect(run(page('<Card className="p-4" />'), baseline)).toHaveLength(1);
  });
});

describe("a component's module", () => {
  it("checks the demo host module without allowing other demo CSS", () => {
    const host = {
      "packages/boards/demo/main.tsx":
        'import styles from "./main.module.css"; document.body.classList.add(styles.root);',
      "packages/boards/demo/main.module.css": ".root { margin: 0; }",
    };
    expect(run(host)).toEqual([]);
    expect(run({ ...host, "packages/boards/demo/main.module.css": "body { margin: 0; }" }).join("\n")).toContain(
      ": selector:",
    );
    expect(run({ ...host, "packages/boards/demo/demo.css": "body { margin: 0; }" }).join("\n")).toContain(
      ": global-css:",
    );
    expect(run({ ...host, "packages/boards/demo/other.module.css": ".root {}" }).join("\n")).toContain(
      ": module-place:",
    );
    expect(
      run({ "packages/boards/demo/main.module.css": host["packages/boards/demo/main.module.css"] }).join("\n"),
    ).toContain(": module-owner:");
  });

  // A local alias is measured as the token it stands for, under every value the module gives it.
  it.each([
    { css: ".input {\n  border: 1px solid var(--line2);\n}", fails: true },
    { css: ".input {\n  --edge: var(--line2);\n  border: 1px solid var(--edge);\n}", fails: true },
    {
      css: ".input {\n  --edge: var(--ctl-line);\n}\n.input:hover {\n  --edge: var(--line2);\n}\n.input {\n  border: 1px solid var(--edge);\n}",
      fails: true,
    },
    { css: ".input {\n  --edge: var(--ctl-line);\n  border: 1px solid var(--edge);\n}", fails: false },
  ])("measures an edge through a local alias as directly: fails $fails", ({ css, fails }) => {
    expect(run(component(css)).some((f) => f.includes(": edge-contrast"))).toBe(fails);
  });

  it.each([
    { name: "an element first", css: "div .root { color: var(--t1); }" },
    { name: "another global class", css: ":global(.btn) .root { color: var(--t1); }" },
    { name: "a bare :global", css: ".root :global(.x) { color: var(--t1); }" },
    { name: "an attribute first", css: "@media (width > 1px) { [data-x] .root { color: var(--t1); } }" },
  ])("fails on a selector with $name", ({ css }) => {
    expect(run(component(`.root { color: var(--t1); }\n${css}`)).join("\n")).toMatch(/box\.module\.css:2: selector/);
  });

  // A nested rule or an at-rule the check does not read could hide any selector, so a module writes neither.
  it.each([
    { name: "flat", css: ":global(.foreign) .root {\n  color: var(--t1);\n}" },
    { name: "nested", css: ".root {\n  :global(.foreign) & {\n    color: var(--t1);\n  }\n}" },
    { name: "scoped", css: "@scope (.x) {\n  :global(.foreign) .root {\n    color: var(--t1);\n  }\n}" },
  ])("fails on a foreign selector written $name", ({ css }) => {
    expect(run(component(css)).join("\n")).toMatch(/box\.module\.css:1: selector/);
  });

  it("allows its own classes, states and the root's theme", () => {
    const css = `.root:hover > .a:has(:checked), .root [data-p~="a1"] { color: var(--t1); }
:global(:root[data-theme="dark"]) .root { color: var(--t2); }
@keyframes enter { from { opacity: 0; } }`;
    expect(run(component(css))).toEqual([]);
  });

  it("fails when another file imports it, or it lives outside the components", () => {
    const files = {
      ...component(".root { color: var(--t1); }"),
      "console/src/a.tsx": 'import s from "../../packages/ui/src/components/box.module.css";\n',
    };
    expect(run(files).join("\n")).toContain("console/src/a.tsx:1: module-owner");
    expect(
      run({ "console/src/x.module.css": ".root {}", "console/src/x.tsx": 'import s from "./x.module.css";' }).join(
        "\n",
      ),
    ).toContain("console/src/x.module.css:1: module-place");
  });

  // A literal behind a local custom property, in a var() fallback or in a keyframe is still a literal.
  it.each([
    ".root {\n  --local: #fff;\n  color: var(--local);\n}",
    ".root {\n  color: var(--missing, #fff);\n}",
    "@keyframes pulse {\n  from {\n    color: white;\n  }\n}\n.root {\n  animation: pulse 1s;\n}",
  ])("fails on a colour literal however it is written: %s", (css) => {
    expect(run(component(css)).join("\n")).toMatch(/box\.module\.css:\d+: color/);
  });

  it.each(["#fff", "rgb(0 0 0 / 0.1)", "white", "oklch(0.5 0.1 200)"])("fails on the colour literal %s", (literal) => {
    expect(run(component(`.root {\n  color: ${literal};\n}`)).join("\n")).toContain("box.module.css:2: color");
  });

  it("fails on a className that is not its module's", () => {
    const tsx = `import s from "./box.module.css";\nexport const Box = () => <div className={cn(s.root, "p-4")} />;\n`;
    expect(run(component(".root {}", tsx)).join("\n")).toContain("box.tsx:2: class-name");
  });

  // An input's edge on the board is sand line2, 1.6:1 on the page in light and 1.9:1 in dark.
  it.each([
    { edge: "box-shadow: inset 0 0 0 1px var(--line2)", fails: true },
    { edge: "border: 1px solid var(--line)", fails: true },
    { edge: "outline: 2px solid var(--hover)", fails: true },
    { edge: "box-shadow: inset 0 0 0 1px var(--ctl-line)", fails: false },
    { edge: "outline: 2px solid var(--focus)", fails: false },
    { edge: "box-shadow: inset 0 0 0 var(--bw) var(--line2)", fails: false },
    { edge: "box-shadow: var(--shPop)", fails: false },
    { edge: "box-shadow: var(--sh1)", fails: false },
    { edge: "box-shadow: 0 1px 3px var(--line2)", fails: false },
    // A colour that is no single token (a mix) or a name nothing defines cannot be measured, so it fails.
    { edge: "border: 1px solid var(--accentWash)", fails: true },
    { edge: "border: 1px solid var(--missing)", fails: true },
    // The colour drawn is what is measured: a mix or the text colour is no token, whatever tokens it is made of.
    { edge: "border: 1px solid color-mix(in srgb, var(--ctl-line) 10%, transparent)", fails: true },
    { edge: "border: 1px solid currentColor", fails: true },
    { edge: "border: 1px solid", fails: true },
    { edge: "border: 1px solid transparent", fails: false },
    { edge: "outline: none", fails: false },
    { edge: "box-shadow: none", fails: false },
  ])("$edge: under 3:1 in light or dark fails $fails", ({ edge, fails }) => {
    const failures = run(component(`.input {\n  ${edge};\n}`));
    expect(failures.some((f) => f.includes("box.module.css:2: edge-contrast"))).toBe(fails);
  });
});

it.each([
  "el.innerHTML = x;",
  "el.outerHTML = x;",
  'el.insertAdjacentHTML("beforeend", x);',
  "el.style.cssText = x;",
  'el.setAttribute("style", x);',
  'el["innerHTML"] = x;',
  "const a = <div dangerouslySetInnerHTML={x} />;",
])("fails on %s anywhere in the source", (line) => {
  expect(run({ "packages/app/src/a.tsx": line }).join("\n")).toContain("packages/app/src/a.tsx:1: html");
});

// The tree is the check CI runs: every rule over every file, about ten seconds of parsing.
it("passes on the tree as it is, against its baseline", { timeout: 60_000 }, () => {
  const baseline: Baseline = JSON.parse(readFileSync(baselinePath, "utf8"));
  expect(check(tree(join(import.meta.dirname, "..")), baseline)).toEqual([]);
});

it("exempts marked generated boards, but not hand-written or unrelated files", () => {
  const marker = "/* @generated by pnpm boards; DO NOT EDIT. */\n";
  const css = ".board { color: red; }";
  const baseline: Baseline = { migrated: [], counts: {} };
  expect(check({ "packages/boards/src/Login/board.css": marker + css }, baseline)).toEqual([]);
  expect(check({ "packages/boards/src/manual.css": css }, baseline).length).toBeGreaterThan(0);
  expect(check({ "packages/ui/src/board.css": marker + css }, baseline).length).toBeGreaterThan(0);
});
