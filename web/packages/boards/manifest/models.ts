import type { Region } from "../regions.ts";

// AxApp has no models screen. Take the page column and the card around the key list, with slots for C7's content.
const frame = ".pin.wide.qpages";
const card = ".p-keys .wv.w2 > .qcard";

/** @public loaded by regions.ts from the directory listing */
export default ["AxApp", "AxAppPhone"].flatMap((board): Region[] => [
  { board, name: "ModelsFrame", roots: [frame], children: frame },
  {
    board,
    name: "ModelsCard",
    roots: [card],
    children: card,
    elements: { [card]: { set: { class: "qcard" } } },
  },
]);
