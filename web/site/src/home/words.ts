import type zh from "../locales/zh";

export type BoardKey = keyof typeof zh.board;

// The board's locale keys (packages/boards/regions.ts names them by their text) of the words the page hands its
// regions itself: those the preference engine rewrites in place, and the island's sections.
export const words = {
  headline: "m2e2a3b1e5d",
  lede: "m0d0cee5072",
  signUpForKey: "mcaf724c6bd",
  readDocs: "md006830a9e",
  models: "mc98e118e0a",
  pricing: "m0f4371a7a9",
  docs: "m2687ccdbb1",
  signIn: "m1e2df9c307",
  signUp: "mc4fb62202b",
} as const satisfies Record<string, BoardKey>;

// The page's sections from the top, each with the depth the island shows at its start (Site.dc.html SECS).
export const sections: { name: BoardKey; depth: number }[] = [
  { name: "m6ebdfb798f", depth: 0 },
  { name: "mf3b3924597", depth: 300 },
  { name: "m446d270979", depth: 1000 },
  { name: "mc7765bcffd", depth: 1800 },
  { name: "m97d3aaa40b", depth: 2600 },
  { name: "m45a6d115fd", depth: 3500 },
  { name: "m8c0f8088c6", depth: 4213 },
];
