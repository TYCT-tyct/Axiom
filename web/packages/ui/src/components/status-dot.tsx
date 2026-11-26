import type { ReactNode } from "react";
import { classes } from "../classes";
import s from "./status-dot.module.css";

// StatusDot is a figure with its state beside it, a small dot in the state's colour and then the number (the admin's
// .qsd in a column of rates): where a column of numbers has to line up, a tag's box would not. The words or the number
// carry the meaning; the dot only adds the colour, so it is hidden from a screen reader.
export function StatusDot({
  tone = "ok",
  children,
}: {
  tone?: "ok" | "warn" | "bad" | "neutral";
  children: ReactNode;
}) {
  return (
    <span className={s.root}>
      <i className={classes(s.dot, s[tone])} aria-hidden="true" />
      {children}
    </span>
  );
}
