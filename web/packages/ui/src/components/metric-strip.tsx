import { Slot } from "radix-ui";
import { cloneElement, type Key, type ReactElement, type ReactNode } from "react";
import { board } from "../board-classes";
import { classes } from "../classes";
import s from "./metric-strip.module.css";

export type StripItem = {
  key: Key;
  label: string;
  // The figure as it reads; prefix (a currency sign) and suffix (a unit) are set smaller beside it.
  value: ReactNode;
  prefix?: string;
  suffix?: string;
  // The small line under the figure: what it is compared with, a Delta, a note.
  note?: ReactNode;
  // A link the whole cell becomes, given empty (a router link, or an anchor): the cell's content goes inside it.
  link?: ReactElement<{ children?: ReactNode }>;
};

// MetricStrip is the row of figures at the top of a list page (the admin's adStrip): one card split into cells of a
// label, a figure and a small line, two to a row on a narrow window. A cell with a link opens what it counts.
export function MetricStrip({ label, items }: { label: string; items: readonly StripItem[] }) {
  return (
    <section
      aria-label={label}
      className={classes(board.qcard, s.root)}
      style={{ "--n": items.length } as Record<`--${string}`, number>}
    >
      {items.map((item) => {
        const content: ReactNode = (
          <>
            <span className={s.label}>{item.label}</span>
            <b className={s.value}>
              {item.prefix ? <span className={s.prefix}>{item.prefix}</span> : null}
              {item.value}
              {item.suffix ? <span className={s.suffix}>{item.suffix}</span> : null}
            </b>
            <em className={s.note}>{item.note}</em>
          </>
        );
        return item.link ? (
          <Slot.Root key={item.key} className={classes(s.cell, s.link)}>
            {cloneElement(item.link, undefined, content)}
          </Slot.Root>
        ) : (
          <div key={item.key} className={s.cell}>
            {content}
          </div>
        );
      })}
    </section>
  );
}
