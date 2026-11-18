import { createContext, type ReactNode, useContext } from "react";
import CellRegion from "./AxDS/regions/Cell.tsx";
import PageHeaderRegion from "./AxDS/regions/PageHeader.tsx";
import RowRegion from "./AxDS/regions/Row.tsx";
import SectionHeaderRegion from "./AxDS/regions/SectionHeader.tsx";
import SegmentedRegion from "./AxDS/regions/Segmented.tsx";
import TableRegion from "./AxDS/regions/Table.tsx";

// The design system's parts (manifest/ds.ts) for pages that have no board of their own, typed by what the page gives
// them. Every word they show is the page's: the board's sample words they replace are never asked for. They are drawn
// for the shell's board root (.ax), so they render inside a board shell.
const t = String;

export function PageHeader({ title, description }: { title: ReactNode; description?: ReactNode }) {
  return <PageHeaderRegion t={t} title={title} description={description} />;
}

export function SectionHeader({ title, description }: { title: ReactNode; description?: ReactNode }) {
  return <SectionHeaderRegion t={t} title={title} description={description} />;
}

// Segmented is a choice of three options, as many as the board draws and sizes its sliding mark for (.segi); the chosen
// one is pressed.
export function Segmented<T extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: T;
  options: readonly { value: T; label: string }[];
  onChange: (value: T) => void;
}) {
  const chosen = options.findIndex((option) => option.value === value);
  const values: Record<string, unknown> = { label, sg: chosen };
  for (const [i, option] of options.entries()) {
    values[`sg${i}`] = i === chosen ? "on" : "";
    values[`sgs${i}`] = () => onChange(option.value);
    values[`pressed${i}`] = i === chosen;
  }
  const text = (i: number) => options[i]?.label ?? "";
  return <SegmentedRegion t={t} values={values} text={{ option0: text(0), option1: text(1), option2: text(2) }} />;
}

// The table's columns, as a grid-template-columns value, which its heading row and every row lay their cells on.
const Columns = createContext("");

// Table lists rows under a heading row; `columns` sizes the cells of both. The board's list is a fixed window for its
// slide-in demo; a page's is as long as its rows.
export function Table({
  label,
  columns,
  head,
  children,
}: {
  label: string;
  columns: string;
  head: ReactNode;
  children: ReactNode;
}) {
  const style = `grid-template-columns: ${columns}`;
  return (
    <Columns.Provider value={style}>
      <TableRegion t={t} values={{ label, columns: style, bodyStyle: "height: auto" }} head={head}>
        {children}
      </TableRegion>
    </Columns.Provider>
  );
}

export function Row({ children }: { children: ReactNode }) {
  return (
    <RowRegion t={t} values={{ columns: useContext(Columns) }}>
      {children}
    </RowRegion>
  );
}

// Cell is a cell of a row, or of the heading row with `head`; kind is the board's: an amount (num), code (mono) or
// a quieter text (mut).
export function Cell({ kind, head, children }: { kind?: "num" | "mono" | "mut"; head?: boolean; children: ReactNode }) {
  return (
    <CellRegion t={t} values={{ kind, role: head ? "columnheader" : "cell" }}>
      {children}
    </CellRegion>
  );
}

export { ActionButton, CopyButton } from "./ds-admin-actions.tsx";
export { CheckboxField, FileField, SelectField, SuggestionField } from "./ds-admin-fields.tsx";
export { FilterForm } from "./ds-admin-filter.tsx";
export { CodeBlock, TestProgress } from "./ds-admin-output.tsx";
export { Dialog, TextAreaField, TextField } from "./ds-dialog.tsx";
export { FilterBar, LoadMore, SortableHeader } from "./ds-list.tsx";
export { Empty, ErrorState, Loading, NoPermission } from "./ds-states.tsx";
