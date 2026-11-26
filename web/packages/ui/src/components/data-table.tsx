import { type Key, type ReactNode, useEffect, useState } from "react";
import { useFormat } from "../format";
import s from "./data-table.module.css";
import { RowButton, type SortOrder, Table, Td, Th, Tr } from "./table";

type Column<Row> = {
  key: string;
  header: string;
  cell: (row: Row) => ReactNode;
  // end aligns numbers and amounts to the right, in figures of one width; muted is for secondary values.
  align?: "start" | "end";
  muted?: boolean;
  // A width in pixels, which only a fixed table keeps.
  width?: number;
  // The width from which the column shows (the admin's adtx and phx: a phone keeps the two or three that matter).
  from?: "sm" | "md" | "lg" | "xl" | "2xl";
  // The number a numeric column sorts by; a column with one gets a sort button. A row without a number sorts last
  // from high to low and first from low to high.
  sort?: (row: Row) => number | null | undefined;
};

type Sorting = { key: string; order: Exclude<SortOrder, "none"> } | null;

// A press on a header steps its column through high to low, low to high, and back to the rows' own order (the
// admin's adTbl); a press on another column starts that one at high to low.
const nextSort = (sorting: Sorting, key: string): Sorting =>
  sorting?.key !== key
    ? { key, order: "descending" }
    : sorting.order === "descending"
      ? { key, order: "ascending" }
      : null;

function sorted<Row>(rows: readonly Row[], column: Column<Row> | undefined, sorting: Sorting) {
  const by = column?.sort;
  if (!by || !sorting) return rows;
  const sign = sorting.order === "ascending" ? 1 : -1;
  const value = (row: Row) => by(row) ?? Number.NEGATIVE_INFINITY;
  // toSorted is stable, so rows of one value keep their own order.
  return rows.toSorted((a, b) => {
    const x = value(a);
    const y = value(b);
    return x === y ? 0 : (x < y ? -1 : 1) * sign;
  });
}

// How many rows the table lists: "4" when it lists all of them, "3 / 10" while a filter hides some.
function countText(format: ReturnType<typeof useFormat>, listed: number, total: number) {
  return listed === total ? format.number(total) : `${format.number(listed)} / ${format.number(total)}`;
}

// A cell of a row. The first of a row that opens holds a button, which is how a keyboard opens it.
function Cell<Row>({ column, row, open }: { column: Column<Row>; row: Row; open?: () => void }) {
  const content = column.cell(row);
  return (
    <Td align={column.align === "end" ? "end" : undefined} muted={column.muted} from={column.from}>
      {open ? <RowButton onClick={open}>{content}</RowButton> : content}
    </Td>
  );
}

// DataTable lists rows under named columns, from data: the Table with its columns described once. A long identifier
// goes in the cell as an Id, in a fixed table with the column's width. A column with a sort number can be sorted from
// its header. Given the count of every row (total) and what a row is called (unit), it says how many it lists, as
// "3 / 10" while a filter hides some. flash names a row an edit just changed, which lights once. Given onOpen, a row
// opens its detail (the admin's data-kind rows): a press anywhere on it that is not on a control of its own, and the
// button a keyboard reaches, which is the first column's text, so that column holds text and no control. current names
// the row whose detail is open.
export function DataTable<Row>({
  label,
  columns,
  rows,
  rowKey,
  fixed = false,
  total,
  unit,
  flash,
  onOpen,
  current,
}: {
  label: string;
  columns: readonly Column<Row>[];
  rows: readonly Row[];
  rowKey: (row: Row) => Key;
  fixed?: boolean;
  total?: number;
  unit?: string;
  flash?: Key;
  onOpen?: (row: Row) => void;
  current?: Key;
}) {
  const format = useFormat();
  const [sorting, setSorting] = useState<Sorting>(null);
  const [lit, setLit] = useState(flash);
  useEffect(() => setLit(flash), [flash]);
  const shown = sorted(
    rows,
    columns.find((column) => column.key === sorting?.key),
    sorting,
  );
  const count = total === undefined ? undefined : countText(format, rows.length, total);
  return (
    <>
      {count === undefined ? null : (
        <p className={s.count} role="status">
          {unit ? `${count} ${unit}` : count}
        </p>
      )}
      <Table label={label} fixed={fixed} widths={columns.map((column) => column.width)}>
        <thead>
          <tr>
            {columns.map((column) => (
              <Th
                key={column.key}
                align={column.align === "end" ? "end" : undefined}
                from={column.from}
                sort={column.sort ? (sorting?.key === column.key ? sorting.order : "none") : undefined}
                onSort={column.sort ? () => setSorting(nextSort(sorting, column.key)) : undefined}
              >
                {column.header}
              </Th>
            ))}
          </tr>
        </thead>
        <tbody>
          {shown.map((row) => {
            const key = rowKey(row);
            return (
              <Tr
                key={key}
                flash={lit !== undefined && lit === key}
                onFlashEnd={() => setLit(undefined)}
                onOpen={onOpen && (() => onOpen(row))}
                current={current !== undefined && current === key}
              >
                {columns.map((column, at) => (
                  <Cell
                    key={column.key}
                    column={column}
                    row={row}
                    open={onOpen && at === 0 ? () => onOpen(row) : undefined}
                  />
                ))}
              </Tr>
            );
          })}
        </tbody>
      </Table>
    </>
  );
}
