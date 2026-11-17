// Evaluate the region manifest rather than hashing its shared source: a page only reads its own entries.
import { pathToFileURL } from "node:url";
import { resolve } from "node:path";

const load = (file) => import(pathToFileURL(resolve(file)).href);
const { boardNames } = await load("web/packages/boards/catalog.ts");
const { regions } = await load("web/packages/boards/regions.ts");
const { boards, slices } = await load("web/packages/boards/src/demo.ts");
const inputs = Object.fromEntries(boardNames.map((board) => [board, {
  regions: regions.filter((region) => region.board === board),
  demo: String(boards[board]),
  slices: Object.fromEntries(Object.entries(slices[board] ?? {}).map(([key, value]) => [key, String(value)])),
}]));
console.log(JSON.stringify({ boards: boardNames, inputs }));
