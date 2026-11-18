import { Slot } from "radix-ui";
import type { ReactNode } from "react";
import s from "./text-link.module.css";

// TextLink lends the design's inline link look (the board's .pplink) to the one link it holds: a router link to a
// detail page, or a plain anchor.
export function TextLink({ children }: { children: ReactNode }) {
  return <Slot.Root className={s.root}>{children}</Slot.Root>;
}
