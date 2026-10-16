import type { AdminSchemas } from "@axiom/api";
import { LoadMore } from "@axiom/app";
import { Button, DataState, EmptyState, Icon, PageHeader, Panel, Table, Td, TextField, Th, useFormat } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { FilterBar, PageBody, State, text } from "../parts";
import { usersQuery } from "../queries";

export default function Users() {
  const { t } = useTranslation();
  const filters = useSearch({ from: "/app/users" });
  const users = useInfiniteQuery(usersQuery({ email_prefix: filters.q }));
  const rows = users.data?.pages.flatMap((page) => page.data) ?? [];
  return (
    <>
      <PageHeader title={t("nav.users")} description={t("users.description")} />
      <PageBody>
        <Search q={filters.q} />
        <Panel>
          <DataState
            query={users}
            isEmpty={() => rows.length === 0}
            empty={
              <EmptyState title={filters.q ? t("users.noMatch.title") : t("users.empty.title")}>
                {filters.q ? t("users.noMatch.body") : t("users.empty.body")}
              </EmptyState>
            }
          >
            {() => <UsersTable rows={rows} />}
          </DataState>
          <LoadMore list={users} />
        </Panel>
      </PageBody>
    </>
  );
}

function Search({ q }: { q: string | undefined }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  return (
    <FilterBar values={{ q }} onApply={(values) => navigate({ to: "/users", search: { q: text(values.q) } })}>
      <TextField
        label={t("users.searchLabel")}
        hint={t("users.searchHint")}
        name="q"
        maxLength={254}
        defaultValue={q}
        autoComplete="off"
        spellCheck={false}
        inputMode="email"
      />
    </FilterBar>
  );
}

function UsersTable({ rows }: { rows: AdminSchemas["UserSummary"][] }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <Table label={t("nav.users")}>
      <thead>
        <tr>
          <Th>{t("users.col.email")}</Th>
          <Th>{t("users.col.status")}</Th>
          <Th className="hidden md:table-cell">{t("users.col.tier")}</Th>
          <Th className="hidden lg:table-cell">{t("users.col.mfa")}</Th>
          <Th className="hidden xl:table-cell">{t("users.col.created")}</Th>
          <Th className="w-0" />
        </tr>
      </thead>
      <tbody>
        {rows.map((user) => (
          <tr key={user.id}>
            <Td className="w-full max-w-0 sm:w-auto sm:max-w-72 sm:min-w-40">
              <p className="truncate text-t1" title={user.email}>
                {user.email}
              </p>
            </Td>
            <Td>
              <State tone={user.status === "active" ? "good" : "warn"}>
                {user.status === "active" ? t("users.active") : t("users.disabled")}
              </State>
            </Td>
            <Td className="hidden max-w-40 md:table-cell">
              <span className="block truncate" title={user.tier.name}>
                {user.tier.name}
              </span>
            </Td>
            <Td className="hidden lg:table-cell">
              <State tone={user.mfa_enabled ? "good" : "neutral"}>
                {user.mfa_enabled ? t("users.mfaOn") : t("users.mfaOff")}
              </State>
            </Td>
            <Td className="hidden whitespace-nowrap xl:table-cell">{format.dateTime(new Date(user.created_at))}</Td>
            <Td className="whitespace-nowrap">
              <Button variant="ghost" size="icon" asChild>
                <Link
                  to="/accounts/$accountId"
                  params={{ accountId: user.account_id }}
                  aria-label={t("users.open", { email: user.email })}
                >
                  <Icon name="right" />
                </Link>
              </Button>
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}
