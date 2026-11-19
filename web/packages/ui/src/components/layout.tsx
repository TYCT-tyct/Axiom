import type { ReactNode } from "react";
import { classes } from "../classes";
import s from "./layout.module.css";

type Gap = 4 | 8 | 12 | 16 | 24 | 32;

// Stack puts blocks one under another, gap pixels apart. rule sets the stack apart from what is above it with a
// hairline and the room below it, as a part of a dialog that goes on under the one before.
export function Stack({ gap = 16, rule = false, children }: { gap?: Gap; rule?: boolean; children: ReactNode }) {
  return <div className={classes(s.stack, s[`g${gap}`], rule && s.rule)}>{children}</div>;
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
// single column on a phone. min instead fits as many columns as leave each at least that wide (a form's fields, a
// filter's), so a grid never needs a breakpoint of its own.
export function Grid({
  columns,
  min,
  gap = 16,
  children,
}: ({ columns: 2 | 3 | 4; min?: never } | { min: "12rem" | "16rem"; columns?: never }) & {
  gap?: Gap;
  children: ReactNode;
}) {
  return (
    <div className={classes(s.grid, columns ? s[`c${columns}`] : s[`m${min?.replace("rem", "")}`], s[`g${gap}`])}>
      {children}
    </div>
  );
}
