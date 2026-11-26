import type { ReactNode } from "react";
import { classes } from "../classes";
import s from "./mono.module.css";

// Mono is an identifier or a name the system gives, in the monospaced face (the board's .mono); it may be long, so it
// wraps anywhere. title carries the whole value where the words shown are a shortening of it. A short identifier in a
// table cell clips instead: one line, cut with an ellipsis where its column is narrower, never broken mid-ID.
export function Mono({ children, title, clip = false }: { children: ReactNode; title?: string; clip?: boolean }) {
  return (
    <span className={classes(s.root, clip && s.clip)} title={title}>
      {children}
    </span>
  );
}
