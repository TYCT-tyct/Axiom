import { LoadMore } from "@axiom/app";
import { Card, DataState, DataTable, EmptyState, TableToolbar, Text } from "@axiom/ui";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Columns } from "../list-table";
import { useSelected } from "../row-drawer";
import { type User, type UserFilter, userFilters, useUserList } from "./user-list";

type Select = ReturnType<typeof useSelected>[1];
export type Open = {
  selected: string | undefined;
  select: Select;
  // The user that is open, if it is among those loaded, and the keys of the rows as listed.
  open: User | undefined;
  keys: string[];
};

// The users a search found, as a table whose rows open a drawer: what the users, accounts and roles tabs share, each with
// its own columns and its own drawer, which it makes from what is open. The search is on the server (the start of an
// email), the state filter on what has been loaded, and "load more" asks for the next page. A row is opened by what keyOf
// says: the user's own ID, or the account's.
export function UserTable({
  label,
  unit,
  columns,
  keyOf,
  lede,
  after,
  children,
}: {
  label: string;
  unit: string;
  columns: Columns<User>;
  keyOf: (user: User) => string;
  // A line above the table, and what goes under it.
  lede?: string;
  after?: ReactNode;
  children?: (open: Open) => ReactNode;
}) {
  const { t } = useTranslation();
  const list = useUserList();
  const [selected, select] = useSelected();
  const { query, loaded, rows, q } = list;
  return (
    <>
      {lede ? <Text tone="muted">{lede}</Text> : null}
      <TableToolbar<UserFilter>
        label={label}
        search={{ label: t("users.search"), value: list.text, onChange: list.setText }}
        filter={{
          label: t("users.filter.label"),
          value: list.filter,
          options: userFilters.map((value) => ({ value, label: t(`users.filter.${value}`) })),
          onChange: list.setFilter,
        }}
        shown={rows.length}
        total={loaded.length}
        unit={unit}
      />
      <Card layout="flush">
        <DataState
          query={query}
          isEmpty={() => loaded.length === 0}
          empty={
            <EmptyState title={q ? t("users.noMatch.title") : t("users.empty.title")}>
              {q ? t("users.noMatch.body") : t("users.empty.body")}
            </EmptyState>
          }
        >
          {() =>
            rows.length === 0 ? (
              <EmptyState compact title={t("users.noMatch.title")}>
                {t("users.filter.none")}
              </EmptyState>
            ) : (
              <DataTable
                label={label}
                columns={columns}
                rows={rows}
                rowKey={keyOf}
                onOpen={(user) => select(keyOf(user))}
                selected={selected}
              />
            )
          }
        </DataState>
        <LoadMore list={query} />
      </Card>
      {after}
      {children?.({
        selected,
        select,
        open: loaded.find((user) => keyOf(user) === selected),
        keys: rows.map(keyOf),
      })}
    </>
  );
}
