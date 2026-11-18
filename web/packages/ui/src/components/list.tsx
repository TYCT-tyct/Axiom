import type { ReactNode } from "react";
import s from "./list.module.css";

// List is a run of points that belong together (why a request waits, which prices moved): a list to a screen reader,
// bullets to the eye, at body size in --t1.
export function List({ children }: { children: ReactNode }) {
  return <ul className={s.root}>{children}</ul>;
}

export function ListItem({ children }: { children: ReactNode }) {
  return <li className={s.item}>{children}</li>;
}
