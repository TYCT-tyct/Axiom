import type { Region } from "../regions.ts";

// The parts of the design system board (AxDS) that pages without a board of their own are made of; packages/boards/ds.tsx
// gives each a typed component. A part is cut from the board's own drawing of it, and what the page supplies (titles,
// options, cells, rows) replaces the drawing's sample content. Its rules select under .ax, which the shell's board root
// carries. Only what a page uses is cut: a part no page uses is left in the board until one does.

// The board's sample content, which the demo shows in the slots and rows, in English.
const english = {
  m53fa27b069: "Axiom console design system",
  m98738462d5:
    "Gentle, quiet, easy to use. Every item below is a real component you can press; the properties on the right switch between light, dark and reduced motion.",
  m070a8016fc: "Colour",
  m279c381531:
    "Warm grey Sand is the neutral, the two sets matching level for level. Cyan is only for the selected, the live and Axiom's choice; state colours appear only on states.",
  m0eeecd26f2: "This month",
  m84ad2952a3: "30 days",
  m38eefacbb3: "7 days",
  mda6a376483: "NameKeyThis month",
};

// The board's amount cell, by its place: its class is the page's.
const cell = ".xrs > .xr:nth-child(2) > span:nth-child(3)";

const parts: Region[] = [
  // The board's own title block: the page's h1 and the sentence under it.
  {
    board: "AxDS",
    name: "PageHeader",
    roots: [".dtop"],
    slots: { title: ".dtop h1", description: ".dtop p" },
  },
  // A block's heading and its note, as the board heads each of its sections.
  {
    board: "AxDS",
    name: "SectionHeader",
    roots: [".dsb > .sec2:first-of-type > .bh"],
    slots: { title: ".dsb > .sec2:first-of-type > .bh h2", description: ".dsb > .sec2:first-of-type > .bh p" },
  },
  // A choice of three options, as the board draws it: its buttons' own state and press holes (sg0, sgs0…), the sliding
  // mark's place (sg), and the pressed state for screen readers.
  {
    board: "AxDS",
    name: "Segmented",
    roots: [".seg"],
    text: { option: ".seg > button" },
    elements: {
      ".seg": { set: { role: "group", "aria-label": "{{label}}" } },
      ...Object.fromEntries(
        [0, 1, 2].map((i) => [`.seg > button:nth-of-type(${i + 1})`, { set: { "aria-pressed": `{{pressed${i}}}` } }]),
      ),
    },
  },
  // A table: the heading row and the rows of the page, each a row of cells laid out on the columns the page gives. The
  // board's list is a fixed window for its slide-in demo; a page's list is as long as its rows (bodyStyle).
  {
    board: "AxDS",
    name: "Table",
    roots: [".tb"],
    slots: { head: ".tb > .xh", children: ".xrs" },
    elements: {
      ".tb": { set: { role: "table", "aria-label": "{{label}}" } },
      ".tb > .xh": { set: { role: "row", style: "{{columns}}" } },
      ".xrs": { set: { class: "xrs", role: "rowgroup", style: "{{bodyStyle}}" } },
    },
  },
  {
    board: "AxDS",
    name: "Row",
    roots: [".xrs > .xr:nth-child(2)"],
    children: ".xrs > .xr:nth-child(2)",
    elements: { ".xrs > .xr:nth-child(2)": { set: { class: "xr", role: "row", style: "{{columns}}" } } },
  },
  // A cell, a heading cell included: its role, and the board's kinds of cell text (num, mono, mut).
  {
    board: "AxDS",
    name: "Cell",
    roots: [cell],
    children: cell,
    elements: { [cell]: { set: { class: "{{kind}}", role: "{{role}}" } } },
    classes: { kind: ["num", "mono", "mut"] },
  },
];

/** @public loaded by regions.ts from the directory listing */
export default parts.map((region) => ({ ...region, english }));
