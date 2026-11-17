import { expect, it } from "vitest";
import { type Chunk, closure, deferred } from "./budget.ts";

// A website's module graph: the entry loads the sea at idle and the preferences on a click; both share a helper, the
// preferences reach a further part that brings a drawing and a font, and the sea imports a module the entry already has.
const manifest: Record<string, Chunk> = {
  "index.html": { file: "index.js", isEntry: true, imports: ["_ui.js"], dynamicImports: ["sea.ts", "prefs.ts"] },
  "_ui.js": { file: "ui.js" },
  "sea.ts": { file: "sea.js", isDynamicEntry: true, imports: ["_math.js", "_ui.js"] },
  "prefs.ts": { file: "prefs.js", isDynamicEntry: true, imports: ["_math.js"], dynamicImports: ["dive.ts"] },
  "_math.js": { file: "math.js" },
  "dive.ts": {
    file: "dive.js",
    isDynamicEntry: true,
    imports: ["index.html"],
    assets: ["assets/figure.svg", "assets/face.woff2"],
  },
};

it("counts what loads after the first screen once each, with the assets it brings, and no font", () => {
  const first = closure(manifest, "index.html");
  expect([...first].sort()).toEqual(["_ui.js", "index.html"]);
  const later = deferred(manifest, first);
  expect([...later.chunks].sort()).toEqual(["_math.js", "dive.ts", "prefs.ts", "sea.ts"]);
  expect([...later.assets]).toEqual(["assets/figure.svg"]);
});

it("counts nothing for an app that loads nothing later", () => {
  const only = { "index.html": { file: "index.js", isEntry: true } };
  expect(deferred(only, closure(only, "index.html"))).toEqual({ chunks: new Set(), assets: new Set() });
});
