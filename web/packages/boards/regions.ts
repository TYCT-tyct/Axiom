import { readdirSync } from "node:fs";
import { join } from "node:path";

// One file per page in manifest/, each exporting its regions as default: page cards running side by side each add their
// own file instead of editing one shared list. Selectors refer to the pinned design, never to generated JSX.
const manifest = join(import.meta.dirname, "manifest");
const files = readdirSync(manifest)
  .filter((file) => file.endsWith(".ts"))
  .sort();
export const regions: Region[] = (
  await Promise.all(files.map(async (file) => (await import(join(manifest, file))).default as Region[]))
).flat();

export type Region = {
  board: string;
  name: string;
  roots: string[];
  render?: {
    root: string;
    actions: ([string, ...unknown[]] | { click: string })[];
    props?: Record<string, string>;
    viewport?: { width: number; height: number };
  };
  page?: string;
  omit?: string[];
  children?: string;
  // Elements whose content the page supplies, by prop name, as children does for its own.
  slots?: Record<string, string>;
  repeats?: Record<string, string>;
  text?: Record<string, string>;
  attributes?: Record<string, { selector: string; attribute: string }>;
  // What the product needs on an element that the drawing leaves out, by selector (a root included): another tag, where
  // "link" renders the router link the page passes as `link`; attributes set, whose values may hold {{holes}}; and
  // unless, a boolean hole that leaves the element out while true.
  elements?: Record<string, { tag?: string; set?: Record<string, string>; unless?: string }>;
  classes?: Record<string, string[]>;
  english?: Record<string, string>;
  // Class prefixes of markup an engine inserts into the region at run time (the connect screen's figure, an SVG fetched
  // and parsed into place): the board's rules naming them are kept, applying to anything inside the region.
  inserted?: string[];
};
