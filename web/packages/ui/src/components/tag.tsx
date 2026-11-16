import type { ReactNode } from "react";
import { classes } from "../classes";
import s from "./tag.module.css";

// Tag names a state or a kind in a few words: neutral, ok, warn (also a retry), bad (a failure) or accent (new, own).
export function Tag({
  tone = "neutral",
  children,
}: {
  tone?: "neutral" | "ok" | "warn" | "bad" | "accent";
  children: ReactNode;
}) {
  return <span className={classes(s.root, tone !== "neutral" && s[tone])}>{children}</span>;
}
