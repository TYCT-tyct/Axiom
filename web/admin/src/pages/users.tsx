import type { AdminSchemas } from "@axiom/api";
import { LoadMore } from "@axiom/app";
import { ActionButton, Card, DataState, EmptyState, Field, Icon, PageHead, Table, Td, Th, useFormat } from "@axiom/ui";
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
      <PageHead title={t("nav.users")} lede={t("users.description")} />
      <PageBody>
        <Search q={filters.q} />
        <Card layout="flush">
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
        </Card>
      </PageBody>
    </>
  );
}

function Search({ q }: { q: string | undefined }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  return (
    <FilterBar values={{ q }} onApply={(values) => navigate({ to: "/users", search: { q: text(values.q) } })}>
      <Field
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
          <Th main>{t("users.col.email")}</Th>
          <Th>{t("users.col.status")}</Th>
          <Th>{t("users.col.tier")}</Th>
          <Th>{t("users.col.mfa")}</Th>
          <Th>{t("users.col.created")}</Th>
          <Th shrink />
        </tr>
      </thead>
      <tbody>
        {rows.map((user) => (
          <tr key={user.id}>
            <Td main>{user.email}</Td>
            <Td>
              <State tone={user.status === "active" ? "good" : "warn"}>
                {user.status === "active" ? t("users.active") : t("users.disabled")}
              </State>
            </Td>
            <Td>{user.tier.name}</Td>
            <Td>
              <State tone={user.mfa_enabled ? "good" : "neutral"}>
                {user.mfa_enabled ? t("users.mfaOn") : t("users.mfaOff")}
              </State>
            </Td>
            <Td>{format.dateTime(new Date(user.created_at))}</Td>
            <Td shrink>
              <ActionButton variant="gho" size="sm" asChild>
                <Link
                  to="/accounts/$accountId"
                  params={{ accountId: user.account_id }}
                  aria-label={t("users.open", { email: user.email })}
                >
                  <Icon name="right" />
                </Link>
              </ActionButton>
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}
