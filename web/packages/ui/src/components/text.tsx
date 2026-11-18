import type { ReactNode } from "react";
import { classes } from "../classes";
import s from "./text.module.css";

// Text is a run of words in one of the design's text styles, so a page names a style instead of writing one: its tone
// (default --t1, muted --t2, faint --t3, or a state's text colour), its size (14px body or 12px small), medium weight
// for a sub-heading, tabular figures for numbers, and the element it is (a paragraph, a span, or an h3 under a card's
// title). Long values wrap anywhere rather than widen the page.
export function Text({
  as: Element = "p",
  tone = "default",
  size = "md",
  medium = false,
  numeric = false,
  nowrap = false,
  children,
}: {
  as?: "p" | "span" | "h3" | "div";
  tone?: "default" | "muted" | "faint" | "ok" | "warn" | "bad";
  size?: "sm" | "md";
  medium?: boolean;
  numeric?: boolean;
  nowrap?: boolean;
  children: ReactNode;
}) {
  return (
    <Element
      className={classes(s.root, s[tone], s[size], medium && s.medium, numeric && s.numeric, nowrap && s.nowrap)}
    >
      {children}
    </Element>
  );
}
