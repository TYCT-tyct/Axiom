import type { ReactNode } from "react";
import { classes } from "../classes";
import s from "./table.module.css";

// A table scrolls sideways inside its own box when the window is too narrow, and never widens the page; its header row
// stays, so a 9-column table on a phone is read by scrolling, not by losing columns. The box is a named region in the
// tab order, as keyboard users can only scroll what takes focus. `fixed` lets a table whose columns are all known give
// each a width and truncate long values inside it; without it, columns size from their widest content.
export function Table({ children, label, fixed = false }: { children: ReactNode; label: string; fixed?: boolean }) {
  return (
    // biome-ignore lint/a11y/noNoninteractiveTabindex: Keyboard users need focus to scroll a table region.
    <section className={s.box} aria-label={label} tabIndex={0}>
      <table aria-label={label} className={classes(s.table, fixed && s.fixed)}>
        {children}
      </table>
    </section>
  );
}

// The ways a column lays out its cells: end-aligned figures, a column that takes only what its content needs (row
// buttons), and the column that names the row, which takes the room left and wraps rather than pushing the others off.
// className is the utility classes pages wrote before the redesign; it goes when the last page drops it (#747).
type Column = { align?: "end"; shrink?: boolean; main?: boolean; className?: string };

const column = ({ align, shrink, main, className }: Column) =>
  classes(align === "end" && s.end, shrink && s.shrink, main && s.main, className);

// A header keeps its own line: a narrow numeric column would otherwise break a two-character label one character per
// line, which reads as two columns.
export function Th({ children, ...layout }: { children?: ReactNode } & Column) {
  return (
    <th scope="col" className={column(layout)}>
      {children}
    </th>
  );
}

export function Td({
  children,
  colSpan,
  muted = false,
  mono = false,
  ...layout
}: { children?: ReactNode; colSpan?: number; muted?: boolean; mono?: boolean } & Column) {
  return (
    <td colSpan={colSpan} className={classes(column(layout), muted && s.muted, mono && s.mono)}>
      {children}
    </td>
  );
}
