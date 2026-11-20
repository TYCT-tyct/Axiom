// @vitest-environment jsdom
import { execFileSync } from "node:child_process";
import { copyFileSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { createRequire } from "node:module";
import { join } from "node:path";
import { brotliCompressSync } from "node:zlib";
import type { ElementType } from "react";
import ts from "typescript";
import { build } from "vite";
import { describe, expect, it, vi } from "vitest";
import { cssBlocks } from "../../../scripts/boards/region-css.ts";
import { convertRegion, retag } from "../../../scripts/boards/regions.ts";
import { marker, packageRoot } from "../../../scripts/boards.ts";
import { type Region, regions } from "../regions.ts";
import { readBoard } from "../source.ts";
import { boardStyle } from "./dom.ts";

const require = createRequire(join(packageRoot, "package.json"));
const { createElement } = require("react") as typeof import("react");
const { renderToStaticMarkup } = require("react-dom/server") as typeof import("react-dom/server");
// Execute only the generated, stateless JSX and fixture data. Never evaluate a design script or its demo class.
function evaluate<T>(code: string): T {
  const output = ts.transpileModule(code, {
    compilerOptions: { jsx: ts.JsxEmit.ReactJSX, module: ts.ModuleKind.CommonJS },
  }).outputText;
  const module = { exports: {} };
  const dependencies = (id: string) => {
    if (id.endsWith(".css")) return { default: { scope: "region-scope" } };
    if (id.endsWith("/dom.ts")) return { boardStyle };
    return require(id);
  };
  new Function("require", "module", "exports", output)(dependencies, module, module.exports);
  return module.exports as T;
}

function renderer(files: Record<string, string>, board: string, name: string) {
  const prefix = `${board}/regions/${name}`;
  const component = evaluate<{ default: ElementType }>(files[`${prefix}.tsx`] as string).default;
  const zh = evaluate<{ default: Record<string, string> }>(files[`${prefix}.zh.ts`] as string).default;
  const en = evaluate<{ default: Record<string, string> }>(files[`${prefix}.en.ts`] as string).default;
  expect(Object.keys(en)).toEqual(Object.keys(zh));
  const demo = evaluate<{ demo: (t: (key: string) => string) => object }>(files[`${prefix}.demo.ts`] as string).demo;
  return { component, props: demo((key) => zh[key] as string), zh };
}

function canonical(node: Node): unknown {
  if (node.nodeType === 3) return node.textContent;
  if (node.nodeType !== 1) return undefined;
  const element = node as HTMLElement;
  const attrs = [...element.attributes].filter((a) => a.name !== "ref" && !a.name.startsWith("on"));
  const pairs = attrs
    .map(({ name, value }) => {
      if (name === "class")
        value = value
          .split(/\s+/)
          .filter((c) => c && c !== "region-scope")
          .join(" ");
      if (name === "style")
        value = [...element.style]
          .map((key) => [key, element.style.getPropertyValue(key).trim()])
          .sort()
          .join(";");
      return [name, value];
    })
    .filter(([, value]) => value !== "")
    .sort();
  return [element.localName, pairs, [...element.childNodes].map(canonical).filter((n) => n !== undefined && n !== "")];
}

function fragment(html: string) {
  const element = document.createElement("div");
  element.innerHTML = html;
  return element;
}

async function buildRegions(board: string) {
  vi.stubEnv("NODE_ENV", "production");
  try {
    return await build({
      configFile: false,
      logLevel: "silent",
      build: {
        write: false,
        minify: true,
        cssCodeSplit: true,
        lib: {
          entry: {
            Shell: join(packageRoot, `src/${board}/regions/Shell.tsx`),
            Overview: join(packageRoot, `src/${board}/regions/Overview.tsx`),
          },
          formats: ["es"],
        },
        rollupOptions: { external: ["react", "react/jsx-runtime"] },
      },
    });
  } finally {
    vi.unstubAllEnvs();
  }
}

// The manifest's declared omissions and element changes, made on the drawing by hand, so that the generated region must
// match it in everything else.
function edit(root: Element, region: Region) {
  const within = (selector: string) => [...(root.matches(selector) ? [root] : []), ...root.querySelectorAll(selector)];
  for (const el of (region.omit ?? []).flatMap(within)) el.remove();
  const changes = Object.entries(region.elements ?? {}).flatMap(([selector, change]) =>
    within(selector).map((found) => ({ found, ...change })),
  );
  for (const { found, tag, set = {} } of changes) {
    // The page's link component stands in for an <a> where the region uses no other.
    const el = tag ? retag(found, tag === "link" ? "a" : tag) : found;
    for (const [name, value] of Object.entries(set)) el.setAttribute(name, value);
  }
}

describe("region generation", () => {
  it("passes noJsxLiterals for every generated region", () => {
    const web = join(packageRoot, "../..");
    // Normal lint skips generated regions. Copy them into its scope and check them in one Biome process.
    const proof = mkdtempSync(join(web, "scripts", "region-proof-"));
    try {
      for (const { board, name } of regions) {
        copyFileSync(join(packageRoot, `src/${board}/regions/${name}.tsx`), join(proof, `${board}-${name}.tsx`));
      }
      execFileSync(join(web, "node_modules/.bin/biome"), ["lint", "--only=style/noJsxLiterals", proof], { cwd: web });
    } finally {
      rmSync(proof, { recursive: true, force: true });
    }
  });
  it("keeps ancestor rules, state/pseudo selectors, grouping rules and used keyframes, and drops unrelated rules", () => {
    const markup =
      '<div class="ov"><div class="shell"></div><section class="page"><b class="qtbl {{state}}">中文</b></section><section class="other"><b>other</b></section></div>';
    const css =
      '.ov .qtbl{color:red}.ov .other{color:blue}.ov .qtbl:hover::before{content:"中"}@container ov (max-width:600px){.ov .qtbl{display:none}.other{display:block}}.qtbl.on{animation:pulse 1s}@keyframes pulse{0%{opacity:0}100%{opacity:1}}@keyframes unused{0%{opacity:0}}';
    const files = convertRegion(markup, css, { board: "Test", name: "Page", roots: [".page"] }, marker);
    const result = files["Test/regions/Page.module.css"] as string;
    expect(result).toContain(":global(.ov .qtbl):where(.scope){color:red}");
    expect(result).toContain(":global(.ov .qtbl:hover):where(.scope)::before");
    expect(result).toContain("@container ov (max-width:600px)");
    expect(result).toContain("@keyframes pulse");
    expect(result).not.toMatch(/other|shell|unused/);
    expect(cssBlocks(result)).toHaveLength(5);
    expect(files["Test/regions/Page.tsx"]).not.toMatch(/DCLogic|@ts-nocheck|中文/);
    expect(files["Test/regions/Page.zh.ts"]).toContain("中文");
  });

  it("adds the rules the product needs that the drawing leaves out, tied to the region", () => {
    const region = { board: "Test", name: "Field", roots: [".lbl"], css: { ".box ~ .lbl": "margin-top:16px" } };
    const files = convertRegion('<label class="lbl">Name</label>', ".lbl{margin:0}", region, marker);
    expect(files["Test/regions/Field.module.css"]).toContain(":global(.box ~ .lbl):where(.scope){margin-top:16px}");
  });

  it("keeps the breadcrumb's parent state when its class comes from a template hole", () => {
    const files = convertRegion(
      '<nav class="qbc"><button class="qbcb {{segAc}}"><b>Personal</b></button></nav>',
      ".qbc .qbcb.up b{font-weight:400}.qbc .qbcb.up:hover b{color:red}.qbc .qbcb.other b{color:blue}",
      { board: "Test", name: "Breadcrumb", roots: [".qbc"] },
      marker,
    );
    expect(files["Test/regions/Breadcrumb.module.css"]).toContain(".qbc .qbcb.up b");
    expect(files["Test/regions/Breadcrumb.module.css"]).toContain(".qbc .qbcb.up:hover b");
    expect(files["Test/regions/Breadcrumb.module.css"]).not.toContain(".other");
  });

  it("copies repeated templates in order, including variant shapes and interleaved elements, with typed props and locale keys", () => {
    const markup =
      '<main><b class="row" title="提示">第一行</b><i>between</i><b class="row">第二行<span>子项</span></b><b class="row">第三行</b></main>';
    const files = convertRegion(
      markup,
      ".row{color:red}",
      { board: "Test", name: "Rows", roots: ["main"], repeats: { items: ".row" } },
      marker,
    );
    const { component, props, zh } = renderer(files, "Test", "Rows");
    expect(canonical(fragment(renderToStaticMarkup(createElement(component, props))))).toEqual(
      canonical(fragment(markup)),
    );
    expect(Object.values(zh)).toEqual(expect.arrayContaining(["提示", "第一行", "第二行", "第三行", "子项"]));
    expect(files["Test/regions/Rows.tsx"]).toContain("readonly Items0Item[]");
  });

  it("changes the declared elements' tags and attributes, and draws links with the page's component", () => {
    const markup =
      '<div class="m"><a class="ni" onclick="{{go}}">日志</a><div class="pmi" onclick="{{out}}">退出</div><b>soon</b><main class="scr"></main></div>';
    const elements = {
      ".ni": { tag: "link", set: { href: "{{to}}", hidden: "{{gone}}" } },
      ".pmi": { tag: "button", set: { type: "button" } },
      main: { set: { id: "main", tabindex: "-1" } },
      b: { unless: "unbuilt" },
    };
    const region = { board: "Test", name: "Edits", roots: [".m"], elements, slots: { mark: "main" } };
    const files = convertRegion(markup, "", region, marker);
    const { component, props } = renderer(files, "Test", "Edits");
    const link = (attributes: object) => createElement("a", { ...attributes, "data-router": "" });
    const mark = createElement("img", { alt: "" });
    const render = (gone: boolean) =>
      fragment(
        renderToStaticMarkup(createElement(component, { ...props, link, mark, values: { to: "/calls", gone } })),
      );
    const shown = render(false);
    expect(shown.querySelector("main > img")).not.toBeNull();
    expect(shown.querySelector("a.ni[data-router]")?.getAttribute("href")).toBe("/calls");
    expect(shown.querySelector("a.ni")?.hasAttribute("hidden")).toBe(false);
    expect(shown.querySelector(".pmi")?.outerHTML).toMatch(/^<button [^>]*type="button"/);
    expect(shown.querySelector("main")?.getAttribute("tabindex")).toBe("-1");
    expect(render(true).querySelector("a.ni")?.hasAttribute("hidden")).toBe(true);
    // Left out while its hole is true, drawn as the drawing has it otherwise.
    expect(shown.querySelector("b")).not.toBeNull();
    const without = renderToStaticMarkup(createElement(component, { ...props, link, values: { unbuilt: true } }));
    expect(fragment(without).querySelector("b")).toBeNull();
    expect(() =>
      convertRegion(markup, "", { board: "Test", name: "Edits", roots: [".m"], elements: { ".none": {} } }, marker),
    ).toThrow(".none matches nothing");
  });

  it.each(["AxApp", "AxAppPhone"])(
    "renders %s shell plus overview with the original DOM, SVG, styles and text",
    (board) => {
      const html = readBoard(join(packageRoot, "inputs", `${board}.dc.html`));
      const markup = html.match(/<\/helmet>([\s\S]*?)<\/x-dc>/)?.[1] as string;
      const manifest = regions.filter(
        (region) => region.board === board && !region.render && (region.name === "Shell" || region.name === "Overview"),
      );
      const drawn = fragment(markup);
      const overview = drawn.querySelector(".p-ov") as Element;
      (drawn.querySelector(".qpages") as Element).replaceChildren();
      edit(drawn.firstElementChild as Element, manifest.find((region) => region.name === "Shell") as Region);
      edit(overview, manifest.find((region) => region.name === "Overview") as Region);
      const original = fragment(drawn.innerHTML.replace(/\{\{[^}]*\}\}/g, ""));
      (original.querySelector(".qpages") as Element).replaceChildren(
        fragment(overview.outerHTML.replace(/\{\{[^}]*\}\}/g, "")).firstChild as Node,
      );
      const files: Record<string, string> = {};
      // Each conversion parses the full board, so convert only the regions rendered in this comparison.
      for (const region of manifest) Object.assign(files, convertRegion(markup, "", region, marker));
      const shell = renderer(files, board, "Shell");
      const page = renderer(files, board, "Overview");
      const actual = renderToStaticMarkup(
        createElement(shell.component, shell.props, createElement(page.component, page.props)),
      );
      expect(canonical(fragment(actual).firstElementChild as Node)).toEqual(
        canonical(original.firstElementChild as Node),
      );
    },
    30_000,
  );

  it.each(["AxApp", "AxAppPhone"])(
    "builds only %s shell and overview, reports brotli bytes, and excludes demo logic and per-region CSS",
    async (board) => {
      const result = await buildRegions(board);
      const output = (Array.isArray(result) ? result[0] : result) as {
        output: {
          type: string;
          code?: string;
          source?: string | Uint8Array;
          fileName: string;
          modules?: Record<string, unknown>;
        }[];
      };
      const js = output.output.filter((file) => file.type === "chunk");
      const css = output.output.filter((file) => file.fileName.endsWith(".css"));
      const modules = js.flatMap((file) => Object.keys(file.modules ?? {})).join("\n");
      expect(modules).not.toMatch(/Board\.tsx|logic\.ts|runtime\.tsx|\.demo\.ts|\/engines\//);
      expect(css).toEqual([]);
      const jsBytes = js.reduce((sum, file) => sum + brotliCompressSync(file.code as string).byteLength, 0);
      const sheet = readFileSync(join(packageRoot, "src/AxApp/regions.css"), "utf8");
      const cssBytes = brotliCompressSync(sheet).byteLength;
      console.log(
        `${board} Shell + Overview brotli: JS ${jsBytes} bytes; shared CSS ${cssBytes} bytes (React external)`,
      );
      expect(jsBytes).toBeLessThan(50_000);
      const count = (text: string): number =>
        cssBlocks(text).reduce((sum, rule) => sum + (rule.selector.startsWith("@") ? count(rule.body) : 1), 0);
      const sheetCount = count(sheet);
      const fullCount = count(readFileSync(join(packageRoot, `src/${board}/board.css`), "utf8"));
      console.log(`Shared AxApp CSS rules: ${sheetCount}/${fullCount}`);
      expect(sheetCount).toBeLessThan(fullCount);
      if (board === "AxAppPhone") {
        expect(readFileSync(join(packageRoot, "src/AxAppPhone/regions/Overview.tsx"), "utf8")).toContain(
          'export { default, type Props } from "../../AxApp/regions/Overview.tsx";',
        );
      }
    },
    30_000,
  );
});
