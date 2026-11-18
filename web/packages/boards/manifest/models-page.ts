import type { Region } from "../regions.ts";

// ModelsPhone repeats the markup and CSS with the m layout active. Keep a single frame so resizing does not remount
// the shared filter form. The board's routes, statistics and sea belong to the demo.
const frame: Region = {
  board: "Models",
  name: "ModelsPage",
  roots: [".board.mx"],
  omit: [".layer", ".stats", ".pnl", ".lst > :not(.flt)", ".leads", ".pick", ".sr"],
  elements: {
    ".board.mx": { set: { class: "mx {{ layout }}", style: "", "data-models-page": "" } },
    ".frame": { set: { class: "", style: "" } },
    ".sec": { set: { "data-models-body": "" } },
    ".hl > h2": { tag: "h1", set: { class: "serif" } },
    ".lede": { set: { class: "lede" } },
    ".lst": { set: { class: "lst", "data-models-filter-row": "" } },
    ".flt": { set: { class: "tabs flt", role: "none", "aria-label": "", "data-models-filter": "" } },
  },
  classes: { layout: ["d", "m"] },
  slots: { title: ".hl > h1", lede: ".lede", filters: ".flt" },
};

/** @public loaded by regions.ts from the directory listing */
export default [frame];
