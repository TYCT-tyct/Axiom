// The project's monospace stack (--family-mono, @axiom/ui fonts.css), self-hosted first, for what the effects draw on a
// canvas: the same glyphs on every system as the page's own mono text, and one place the stack is written.
export const mono = () =>
  getComputedStyle(document.documentElement).getPropertyValue("--family-mono").trim() || "monospace";
