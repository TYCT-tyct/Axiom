import type { ReactNode } from "react";
import s from "./scroll-area.module.css";

// ScrollArea keeps a list that may run to hundreds of items (a model checklist) to a fixed height in an outlined box,
// scrolling inside; the box is a named region in the tab order, as keyboard users can only scroll what takes focus.
export function ScrollArea({ label, children }: { label: string; children: ReactNode }) {
  return (
    // biome-ignore lint/a11y/noNoninteractiveTabindex: Keyboard users need focus to scroll the box.
    <section className={s.root} aria-label={label} tabIndex={0}>
      {children}
    </section>
  );
}
