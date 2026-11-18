import type { Key, ReactNode } from "react";
import { Table, Td, Th } from "./table";

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

// DataTable lists rows under named columns, from data: the Table with its columns described once. A long identifier
// goes in the cell as an Id, in a fixed table with the column's width.
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
  const align = (column: Column<Row>) => (column.align === "end" ? "end" : undefined);
  return (
    <Table label={label} fixed={fixed} widths={columns.map((column) => column.width)}>
      <thead>
        <tr>
          {columns.map((column) => (
            <Th key={column.key} align={align(column)}>
              {column.header}
            </Th>
          ))}
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => (
          <tr key={rowKey(row)}>
            {columns.map((column) => (
              <Td key={column.key} align={align(column)} muted={column.muted}>
                {column.cell(row)}
              </Td>
            ))}
          </tr>
        ))}
      </tbody>
    </Table>
  );
}
