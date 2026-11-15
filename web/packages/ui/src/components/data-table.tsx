import type { Key, ReactNode } from "react";
import { classes } from "../classes";
import s from "./data-table.module.css";

type Column<Row> = {
  key: string;
  header: string;
  cell: (row: Row) => ReactNode;
  // end aligns numbers and amounts to the right, in figures of one width; muted is for secondary values.
  align?: "start" | "end";
  muted?: boolean;
  // A width in pixels, which only a fixed table keeps.
  width?: number;
};

// DataTable lists rows under named columns. The scrolling box is focusable and named, so a keyboard can scroll a
// table wider than the window. A long identifier goes in the cell as an Id, in a fixed table with the column's width.
export function DataTable<Row>({
  label,
  columns,
  rows,
  rowKey,
  fixed = false,
}: {
  label: string;
  columns: readonly Column<Row>[];
  rows: readonly Row[];
  rowKey: (row: Row) => Key;
  fixed?: boolean;
}) {
  const cell = (column: Column<Row>, head: boolean) =>
    classes(head ? s.th : s.td, column.align === "end" && s.end, !head && column.muted && s.muted);
  return (
    // biome-ignore lint/a11y/noNoninteractiveTabindex: a keyboard needs focus on the box to scroll a wide table.
    <section className={s.scroll} aria-label={label} tabIndex={0}>
      <table className={classes(s.table, fixed && s.fixed)}>
        <caption className={s.caption}>{label}</caption>
        {fixed ? (
          <colgroup>
            {columns.map((column) => (
              <col
                key={column.key}
                className={s.col}
                style={column.width ? ({ "--w": `${column.width}px` } as Record<`--${string}`, string>) : undefined}
              />
            ))}
          </colgroup>
        ) : null}
        <thead>
          <tr>
            {columns.map((column) => (
              <th key={column.key} scope="col" className={cell(column, true)}>
                {column.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={rowKey(row)}>
              {columns.map((column) => (
                <td key={column.key} className={cell(column, false)}>
                  {column.cell(row)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}
