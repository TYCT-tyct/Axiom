import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { expect, it } from "vitest";

// Tailwind writes only the classes it finds in the files its stylesheet names with @source, and it skips node_modules,
// where the workspace packages are linked. A package whose components carry classes must be named by every app that
// uses it, or those classes silently do nothing in that app (the sidebar's footer, the QR code's colours).

const web = resolve(import.meta.dirname, "..");
const sources = (css: string) =>
  [...readFileSync(css, "utf8").matchAll(/@source "([^"]+)";/g)].map((m) => resolve(dirname(css), m[1] as string));
const hasComponents = (dir: string) =>
  readdirSync(dir, { recursive: true }).some((file) => String(file).endsWith(".tsx"));

it.each(["site", "console", "admin"])("%s's stylesheet names every package with components it uses", (app) => {
  const named = [...sources(join(web, app, "src/main.css")), ...sources(join(web, "packages/ui/src/styles.css"))];
  const used = Object.keys(JSON.parse(readFileSync(join(web, app, "package.json"), "utf8")).dependencies ?? {})
    .filter((name) => name.startsWith("@axiom/"))
    .map((name) => join(web, "packages", name.slice("@axiom/".length), "src"))
    .filter(hasComponents);
  expect(used.filter((dir) => !named.includes(dir))).toEqual([]);
});
