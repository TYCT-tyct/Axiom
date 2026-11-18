import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { expect, it } from "vitest";
import type { Region } from "../packages/boards/regions.ts";
import { convertRegion } from "./boards/regions.ts";
import { renderRegions } from "./boards/render.ts";

it("converts a browser-drawn subtree deterministically and reads changes in its source script", async () => {
  const directory = mkdtempSync(join(tmpdir(), "axiom-rendered-region-"));
  const region: Region = {
    board: "Drawn",
    name: "Drawn",
    roots: [".page"],
    render: { root: ".page", actions: [["go", "logs"], { click: ".draw" }] },
    repeats: { items: ".row" },
    text: { heading: "h1" },
    elements: { ".draw": { set: { onclick: "{{click}}" } } },
  };
  const source = `<x-dc><helmet><style>*{font-family:sans-serif}.host .page{color:red}.other{color:blue}</style></helmet>
<main class="host {{rootC}}"><section class="page" ref="{{root}}" data-k="name"></section><aside class="other"></aside></main>
</x-dc><script data-dc-script>class Component extends DCLogic {
  root = el => { this.el = el; };
  renderVals() { return {root: this.root, rootC: 'lvP'}; }
  go(page) {
    this.el.innerHTML = '<h1>' + page + '</h1><button class="draw">Draw</button>';
    this.el.querySelector('button').addEventListener('click', () => {
      this.el.insertAdjacentHTML('beforeend', '<b class="row">中文 ' + Math.random() + '</b><b class="row">' + Date.now() + '</b>');
    });
  }
}</script>`;
  try {
    const file = join(directory, "Drawn.dc.html");
    writeFileSync(file, source);
    const draw = async () => (await renderRegions(directory, [region])).get(region) as string;
    const first = await draw();
    expect(await draw()).toBe(first);
    expect(first).toContain('class="host lvP"');
    expect(first).toContain('data-k="name"');
    expect(first).not.toMatch(/data-design-key|data-ref|data-event-/);
    expect(first).toContain("1792814400000");
    const files = convertRegion(
      first,
      ".host .page{color:red}.thD .page{color:white}.other{color:blue}",
      region,
      "generated",
    );
    expect(files["Drawn/regions/Drawn.module.css"]).toContain(".host .page");
    expect(files["Drawn/regions/Drawn.module.css"]).not.toContain(".other");
    expect(files["Drawn/regions/Drawn.module.css"]).toContain(".thD .page");
    expect(files["Drawn/regions/Drawn.tsx"]).not.toContain("data-design-class");
    expect(files["Drawn/regions/Drawn.tsx"]).toContain("onClick={v.click}");
    expect(files["Drawn/regions/Drawn.tsx"]).toContain("rows.items0.map");
    expect(files["Drawn/regions/Drawn.demo.ts"]).toContain('heading: "logs"');
    expect(files["Drawn/regions/Drawn.zh.ts"]).toContain("中文");
    writeFileSync(file, source.replace("中文", "更新"));
    const changed = convertRegion(await draw(), "", region, "generated");
    expect(changed["Drawn/regions/Drawn.demo.ts"]).not.toBe(files["Drawn/regions/Drawn.demo.ts"]);
    expect(changed["Drawn/regions/Drawn.zh.ts"]).toContain("更新");
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
}, 30_000);
