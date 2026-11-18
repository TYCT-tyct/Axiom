import type { ReactNode } from "react";
import { classes } from "../classes";
import { Icon, type IconName } from "./icon";
import s from "./text.module.css";

// Text is a run of words in one of the design's text styles, so a page names a style instead of writing one: its tone
// (default --t1, muted --t2, faint --t3, or a state's text colour), its size (14px body or 12px small), medium weight
// for a sub-heading, tabular figures for numbers, and the element it is (a paragraph, a span, or an h3 under a card's
// title), with an optional icon before the words. Long values wrap anywhere rather than widen the page; lines cuts a
// long text to two lines with an ellipsis, the whole of it still in the DOM for a screen reader and in title for hover.
export function Text({
  as: Element = "p",
  tone = "default",
  size = "md",
  medium = false,
  numeric = false,
  nowrap = false,
  lines,
  title,
  icon,
  children,
}: {
  as?: "p" | "span" | "h3" | "div";
  tone?: "default" | "muted" | "faint" | "ok" | "warn" | "bad";
  size?: "sm" | "md";
  medium?: boolean;
  numeric?: boolean;
  nowrap?: boolean;
  lines?: 2;
  title?: string;
  icon?: IconName;
  children: ReactNode;
}) {
  return (
    <Element
      className={classes(
        s.root,
        s[tone],
        s[size],
        medium && s.medium,
        numeric && s.numeric,
        nowrap && s.nowrap,
        lines && s.lines2,
        icon && s.withIcon,
      )}
      title={title}
    >
      {icon ? <Icon name={icon} className={s.icon} /> : null}
      {children}
    </Element>
  );
}
