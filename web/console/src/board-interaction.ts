import { boardMedia } from "@axiom/ui";

// The shell uses the tablet board from 820px, with its sidebar folded below 1024px.
export const phoneWidth = boardMedia("(max-width: 819px)");
export const wideWidth = boardMedia("(min-width: 1024px)");
