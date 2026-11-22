import type { OceanEngine, Way } from "@axiom/fx/login";
import { createContext } from "react";

// LoginHost is a page that holds the sign-in pages in its own document. The homepage's frame (.tr) has the Login board
// stand in it beside the homepage, so the tiles transition between them is one scene (port design §9, trans.js): the
// sea is the host's, which the page's solid joins, reading which way the transition runs, and the page tells the host
// when its frame is laid out with its question and its solid on the sea, so the transition can measure it and start.
// A host with no sea (the models page) has the frame bring its own. Either takes the ways out (the brand, Esc, back),
// which in the console's own document return-home.ts takes.
export type LoginHost = {
  root: HTMLElement;
  sea: OceanEngine | null;
  way: Way;
  ready: (lg: HTMLElement) => void;
};

export const LoginHostContext = createContext<LoginHost | null>(null);
