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
  // A small picture beside the figure, top right: the admin's trend line (a Sparkline).
  trend?: ReactNode;
  // A link the whole cell becomes, given empty (a router link, or an anchor): the cell's content goes inside it.
  link?: ReactElement<{ children?: ReactNode }>;
};

// MetricStrip is the row of figures at the top of a list page (the admin's adStrip): one card split into cells of a
// label, a figure and a small line, two to a row on a narrow window. A cell with a link opens what it counts. With
// columns, the cells wrap to rows of that many (the dashboard's eight in two rows of four, the monitor's six in two of
// three), split by hairlines both ways.
export function MetricStrip({
  label,
  items,
  columns,
}: {
  label: string;
  items: readonly StripItem[];
  columns?: 3 | 4;
}) {
  return (
    <section
      aria-label={label}
      className={classes(board.qcard, s.root, columns && s[`c${columns}`])}
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
            {item.trend ? <span className={s.trend}>{item.trend}</span> : null}
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
