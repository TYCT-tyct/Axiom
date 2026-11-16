import type { ReactNode } from "react";
import { classes } from "../classes";
import s from "./layout.module.css";

type Gap = 4 | 8 | 12 | 16 | 24 | 32;

// Stack puts blocks one under another, gap pixels apart.
export function Stack({ gap = 16, children }: { gap?: Gap; children: ReactNode }) {
  return <div className={classes(s.stack, s[`g${gap}`])}>{children}</div>;
}

// Cluster puts small things in a row that wraps (tags, buttons), spread to both ends or packed to the end if asked.
export function Cluster({
  gap = 8,
  justify = "start",
  children,
}: {
  gap?: Gap;
  justify?: "start" | "between" | "end";
  children: ReactNode;
}) {
  return <div className={classes(s.cluster, s[`g${gap}`], justify !== "start" && s[justify])}>{children}</div>;
}

// Grid puts blocks in equal columns: two or three stay so down to a tablet, four become two there, and every grid is a
// single column on a phone.
export function Grid({ columns, gap = 16, children }: { columns: 2 | 3 | 4; gap?: Gap; children: ReactNode }) {
  return <div className={classes(s.grid, s[`c${columns}`], s[`g${gap}`])}>{children}</div>;
}
