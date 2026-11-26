import type { ReactNode } from "react";
import { board } from "../board-classes";
import { classes } from "../classes";
import { Icon, type IconName } from "./icon";
import s from "./tag.module.css";

const tones = {
  neutral: s.neutral,
  ok: s.ok,
  warn: s.warn,
  bad: s.bad,
  accent: s.accent,
} as const;

// Tag names a state or a kind in a few words (the board's .qtag shape). A state sits on an opaque tint of its colour
// with words tuned to about 5:1 on it (the admin's adDot), so a tag reads the same on a hovered or selected row. dot
// adds the state's dot before the words, for a status; an icon may stand beside the words, never for them.
export function Tag({
  tone = "neutral",
  dot = false,
  icon,
  children,
}: {
  tone?: keyof typeof tones;
  dot?: boolean;
  icon?: IconName;
  children: ReactNode;
}) {
  return (
    <span className={classes(board.qtag, tones[tone], dot && s.dotted, icon && s.withIcon)}>
      {dot ? <i className={s.dot} aria-hidden="true" /> : null}
      {icon ? <Icon name={icon} width="12" height="12" aria-hidden="true" /> : null}
      {children}
    </span>
  );
}
