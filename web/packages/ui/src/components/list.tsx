import type { ReactNode } from "react";
import s from "./list.module.css";

// List is a run of points that belong together (why a request waits, which prices moved): a list to a screen reader,
// bullets to the eye, at body size in --t1.
// ordered numbers the points, for steps taken in turn; plain drops the bullets where each item draws itself (a row).
export function List({
  ordered = false,
  plain = false,
  children,
}: {
  ordered?: boolean;
  plain?: boolean;
  children: ReactNode;
}) {
  const Element = ordered ? "ol" : "ul";
  return <Element className={plain ? s.plain : ordered ? s.ordered : s.root}>{children}</Element>;
}

export function ListItem({ children }: { children: ReactNode }) {
  return <li className={s.item}>{children}</li>;
}
