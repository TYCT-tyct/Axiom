/// <reference types="node" />
import { readFileSync } from "node:fs";
import { join } from "node:path";

// Test helpers that read a component's CSS Module as text, without its comments. Read as a file: vitest resolves a
// module stylesheet with ?raw to its class map in some runs, not to its text.
export const moduleCss = (name: string) =>
  readFileSync(join(import.meta.dirname, `${name}.module.css`), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

// reduced is the stylesheet with every @media (prefers-reduced-motion: no-preference …) block taken out: what applies
// under reduced motion.
export function reduced(text: string) {
  const head = "@media (prefers-reduced-motion: no-preference)";
  let out = text;
  for (let at = out.indexOf(head); at >= 0; at = out.indexOf(head)) {
    let depth = 0;
    let end = out.indexOf("{", at);
    do {
      depth += out[end] === "{" ? 1 : out[end] === "}" ? -1 : 0;
      end++;
    } while (depth > 0);
    out = out.slice(0, at) + out.slice(end);
  }
  return out;
}
