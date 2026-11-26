import { LoadMore } from "@axiom/app";
import { Card, DataState, DataTable, EmptyState, TableToolbar, type useFormat } from "@axiom/ui";
import type { ComponentProps, Key } from "react";
import { useTranslation } from "react-i18next";

// What a table's columns are, and the formats a column's cells write dates and amounts with.
export type Columns<T> = ComponentProps<typeof DataTable<T>>["columns"];
export type Format = ReturnType<typeof useFormat>;

type Query = ComponentProps<typeof LoadMore>["list"] & ComponentProps<typeof DataState>["query"];

// ListTable is a list page's table (the admin's adTbl): the toolbar over it, the table in a card, and "load more" under it.
// What the search and the filter narrow, and how, is the page's: it passes the rows that are left and how many were loaded,
// so the toolbar can say "3 / 10". With onOpen a row opens its drawer, and selected is the open one's key.
export function ListTable<T, V extends string = string>({
  label,
  unit,
  search,
  filter,
  query,
  rows,
  loaded,
  columns,
  rowKey,
  selected,
  onOpen,
  empty,
  narrowed,
}: {
  label: string;
  unit: string;
  search: { label: string; value: string; onChange: (value: string) => void };
  filter?: ComponentProps<typeof TableToolbar<V>>["filter"];
  query: Query;
  rows: readonly T[];
  loaded: number;
  columns: ComponentProps<typeof DataTable<T>>["columns"];
  rowKey: (row: T) => Key;
  selected?: string;
  onOpen?: (row: T) => void;
  empty: { title: string; body: string };
  // Whether a search or a filter is on, which is why a list with nothing in it has nothing in it.
  narrowed: boolean;
}) {
  const { t } = useTranslation();
  return (
    <>
      <TableToolbar<V> label={label} search={search} filter={filter} shown={rows.length} total={loaded} unit={unit} />
      <Card layout="flush">
        <DataState
          query={query}
          isEmpty={() => rows.length === 0}
          empty={
            narrowed ? (
              <EmptyState title={t("list.noMatch.title")}>{t("list.noMatch.body")}</EmptyState>
            ) : (
              <EmptyState title={empty.title}>{empty.body}</EmptyState>
            )
          }
        >
          {() => (
            <DataTable
              label={label}
              columns={columns}
              rows={rows}
              rowKey={rowKey}
              selected={selected}
              onOpen={onOpen}
            />
          )}
        </DataState>
        <LoadMore list={query} />
      </Card>
    </>
  );
}
