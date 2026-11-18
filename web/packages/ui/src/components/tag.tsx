import type { ReactNode } from "react";
import { classes } from "../classes";
import { Icon, type IconName } from "./icon";
import s from "./tag.module.css";

// Tag names a state or a kind in a few words: neutral, ok, warn (also a retry), bad (a failure) or accent (new, own).
// An icon may stand beside the words, never for them.
export function Tag({
  tone = "neutral",
  icon,
  children,
}: {
  tone?: "neutral" | "ok" | "warn" | "bad" | "accent";
  icon?: IconName;
  children: ReactNode;
}) {
  return (
    <span className={classes(s.root, tone !== "neutral" && s[tone])}>
      {icon ? <Icon name={icon} width="12" height="12" aria-hidden="true" /> : null}
      {children}
    </span>
  );
}
