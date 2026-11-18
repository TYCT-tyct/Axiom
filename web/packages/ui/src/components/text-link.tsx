import { Slot } from "radix-ui";
import type { ReactNode } from "react";
import { classes } from "../classes";
import s from "./text-link.module.css";

// TextLink lends the design's inline link look (the board's .pplink) to the one link it holds: a router link to a
// detail page, or a plain anchor. danger marks a link that leads to a destructive step.
export function TextLink({ danger = false, children }: { danger?: boolean; children: ReactNode }) {
  return <Slot.Root className={classes(s.root, danger && s.danger)}>{children}</Slot.Root>;
}
