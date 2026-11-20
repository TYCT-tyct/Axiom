import type { ReactNode } from "react";
import { board } from "../board-classes";
import { classes } from "../classes";
import { Icon, type IconName } from "./icon";
import s from "./tag.module.css";

const tones = {
  neutral: undefined,
  ok: board.ok,
  warn: board.warn,
  bad: board.fail,
  accent: board.beta,
} as const;

// Tag names a state or a kind in a few words (.qtag ok/warn/fail/beta from AxApp/regions.css). An icon may stand
// beside the words, never for them.
export function Tag({
  tone = "neutral",
  icon,
  children,
}: {
  tone?: keyof typeof tones;
  icon?: IconName;
  children: ReactNode;
}) {
  return (
    <span className={classes(board.qtag, tones[tone], icon && s.withIcon)}>
      {icon ? <Icon name={icon} width="12" height="12" aria-hidden="true" /> : null}
      {children}
    </span>
  );
}
