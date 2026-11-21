// Only the boards the product's pages are cut from. The design's other artboards (dark, tablet and narrower variants,
// single-screen boards) stay in the design canvas: their converted copies were proofs of the converter alone.
export const boardNames = ["Login", "AxApp", "Site", "SitePhone", "Models", "AxAppPhone", "AxDS"] as const;

export const engineNames = [
  "ocean-orig",
  "login-ui",
  "clawd",
  "clawd-egg",
  "ocean-react",
  "respond",
  "whale",
  "whale-egg",
  "clawd-cast",
  "trans",
  "prefs",
  "hero-enter",
  "eggs",
  "routes",
];

export function boardEngines(name: string, html: string): string[] {
  // Preserve the original three public components and their module graphs.
  if (name === "AxApp") return [];
  return [...html.matchAll(/<script[^>]*src="\.\/([\w-]+)\.js"/g)]
    .map((match) => match[1] as string)
    .filter((engine) => engine !== "support");
}
