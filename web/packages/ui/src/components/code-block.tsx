import type { ReactNode } from "react";
import { classes } from "../classes";
import s from "./code-block.module.css";

// CodeBlock shows text the user copies by hand: a secret shown once (code, selected whole by one click), a snippet to
// paste somewhere (pre, which keeps its lines), or a set of short codes (ul, one per item, two columns when there is
// room).
export function CodeBlock({
  as: Element = "code",
  size = "md",
  children,
}: {
  as?: "code" | "pre" | "ul";
  size?: "sm" | "md";
  children: ReactNode;
}) {
  return (
    <Element className={classes(s.root, size === "sm" && s.sm, Element === "pre" && s.pre, Element === "ul" && s.list)}>
      {children}
    </Element>
  );
}
