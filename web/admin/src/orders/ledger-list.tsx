import { LoadMore } from "@axiom/app";
import { DataState, EmptyState, Panel, Table } from "@axiom/ui";
import { Fragment, type ReactNode } from "react";
import { Head } from "../parts";
import { LedgerHeading } from "./ledger-heading";

type Page<T> = { data?: T[] };
type LedgerQuery<T> = {
  status: "pending" | "error" | "success";
  data: { pages: Page<T>[] } | undefined;
  error: unknown;
  errorUpdatedAt: number;
  refetch: () => unknown;
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  fetchNextPage: () => unknown;
};

// The common shape of the orders page's ledger views: a heading, the data state, a table and the next-page button.
// Each view supplies its own columns and row, and the query keeps the list's paging controls.
export function LedgerList<T>({
  title,
  description,
  back,
  query,
  rows,
  emptyTitle,
  emptyBody,
  columns,
  renderRow,
  keyOf,
}: {
  title: string;
  description: string;
  back: string;
  query: LedgerQuery<T>;
  rows: T[];
  emptyTitle: string;
  emptyBody: string;
  columns: [string, string?][];
  renderRow: (item: T) => ReactNode;
  keyOf: (item: T, index: number) => string | number;
}) {
  return (
    <Panel title={title}>
      <LedgerHeading description={description} back={back} />
      <DataState
        query={query}
        isEmpty={() => rows.length === 0}
        empty={<EmptyState title={emptyTitle}>{emptyBody}</EmptyState>}
      >
        {() => (
          <Table label={title}>
            <Head cols={columns} />
            <tbody>
              {rows.map((item, index) => (
                <Fragment key={keyOf(item, index)}>{renderRow(item)}</Fragment>
              ))}
            </tbody>
          </Table>
        )}
      </DataState>
      <LoadMore list={query} />
    </Panel>
  );
}
