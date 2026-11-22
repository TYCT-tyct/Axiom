import type zh from "../locales/zh";

export type BoardKey = keyof typeof zh.board;

// The board's locale keys (packages/boards/manifest/site.ts names them by their text) of the words the page hands its
// regions itself: those the preference engine rewrites in place.
export const words = {
  headline: "m2e2a3b1e5d",
  lede: "m0d0cee5072",
  signInForKey: "m980e5e70bd",
  models: "mc98e118e0a",
  pricing: "m0f4371a7a9",
  signIn: "m1e2df9c307",
  signUp: "mc4fb62202b",
} as const satisfies Record<string, BoardKey>;

// The page's sections from the top, each with the depth the island shows at its start (Site.dc.html SECS).
export const sections: { name: keyof typeof zh.sections; depth: number }[] = [
  { name: "surface", depth: 0 },
  { name: "connect", depth: 300 },
  { name: "models", depth: 1000 },
  { name: "steps", depth: 1800 },
  { name: "production", depth: 2600 },
  { name: "faq", depth: 3500 },
  { name: "seabed", depth: 4213 },
];
