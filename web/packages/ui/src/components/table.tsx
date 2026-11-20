import type { ReactNode } from "react";
import { classes } from "../classes";
import s from "./table.module.css";

// A table scrolls sideways inside its own box when the window is too narrow, and never widens the page; its header row
// stays, so a 9-column table on a phone is read by scrolling, not by losing columns. The box is a named region in the
// tab order, as keyboard users can only scroll what takes focus. `fixed` lets a table whose columns are all known give
// each a width and truncate long values inside it; without it, columns size from their widest content.
// widths gives a fixed table's columns their widths in pixels, in order (none: the column shares what is left).
export function Table({
  children,
  label,
  fixed = false,
  widths,
}: {
  children: ReactNode;
  label: string;
  fixed?: boolean;
  widths?: readonly (number | undefined)[];
}) {
  return (
    // biome-ignore lint/a11y/noNoninteractiveTabindex: Keyboard users need focus to scroll a table region.
    <section className={s.box} aria-label={label} tabIndex={0}>
      <table aria-label={label} className={classes(s.table, fixed && s.fixed)}>
        {fixed && widths ? (
          <colgroup>
            {widths.map((width, i) => (
              <col
                // biome-ignore lint/suspicious/noArrayIndexKey: columns are positional.
                key={i}
                className={s.col}
                style={width ? ({ "--w": `${width}px` } as Record<`--${string}`, string>) : undefined}
              />
            ))}
          </colgroup>
        ) : null}
        {children}
      </table>
    </section>
  );
}

// The ways a column lays out its cells: end-aligned figures, a column that takes only what its content needs, the
// column that names the row, which takes the room left and wraps rather than pushing the others off, and the row's
// buttons (actions), which stay at the box's right edge while the table scrolls under them, so they are never off
// screen. A column given `from` is dropped below that width (sm 640px, md 768px, lg 1024px, xl 1280px, 2xl 1536px),
// for a table with more columns than a narrow screen has room for.
type Column = {
  align?: "end";
  shrink?: boolean;
  main?: boolean;
  actions?: boolean;
  from?: "sm" | "md" | "lg" | "xl" | "2xl";
};

const column = ({ align, shrink, main, actions, from }: Column) =>
  classes(
    align === "end" && s.end,
    (shrink || actions) && s.shrink,
    main && s.main,
    actions && s.actions,
    from && s[`from${from}`],
  );

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
