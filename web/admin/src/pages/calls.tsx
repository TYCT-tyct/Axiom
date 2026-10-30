import type { AdminSchemas } from "@axiom/api";
import { can, LoadMore, useSession } from "@axiom/app";
import { Button, DataState, EmptyState, Icon, PageHeader, Panel, Table, Td, TextField, useFormat } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Code, FilterBar, Head, mainCell, PageBody, State, Time, text, toInstant, toLocalInput } from "../parts";
import { adminCallsQuery } from "../queries";

// The support view of calls: what a person asks about a bill, without costs, endpoints or routes. The operator's trace
// is a separate page and permission, so this one never shows a field the support role may not read.
export default function Calls() {
  const { t } = useTranslation();
  const filters = useSearch({ from: "/app/calls" });
  const calls = useInfiniteQuery(
    adminCallsQuery({
      account_id: filters.account_id,
      request_id: filters.request_id,
      since: filters.since,
      until: filters.until,
    }),
  );
  const rows = calls.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  const narrowed = Boolean(filters.account_id || filters.request_id || filters.since || filters.until);
  return (
    <>
      <PageHeader title={t("nav.calls")} description={t("calls.description")} />
      <PageBody>
        <Filters filters={filters} />
        <Panel>
          <DataState
            query={calls}
            isEmpty={() => rows.length === 0}
            empty={
              <EmptyState title={narrowed ? t("calls.noMatch.title") : t("calls.empty.title")}>
                {narrowed ? t("calls.noMatch.body") : t("calls.empty.body")}
              </EmptyState>
            }
          >
            {() => <CallsTable rows={rows} />}
          </DataState>
          <LoadMore list={calls} />
        </Panel>
      </PageBody>
    </>
  );
}

function Filters({
  filters,
}: {
  filters: { account_id?: string; request_id?: string; since?: string; until?: string };
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  return (
    <FilterBar
      values={filters}
      onApply={(values) =>
        navigate({
          to: "/calls",
          search: {
            account_id: text(values.account_id),
            request_id: text(values.request_id),
            since: toInstant(values.since),
            until: toInstant(values.until),
          },
        })
      }
    >
      <TextField
        label={t("calls.filters.account")}
        hint={t("calls.filters.accountHint")}
        name="account_id"
        defaultValue={filters.account_id}
        maxLength={64}
        autoComplete="off"
        spellCheck={false}
      />
      <TextField
        label={t("calls.filters.request")}
        hint={t("calls.filters.requestHint")}
        name="request_id"
        defaultValue={filters.request_id}
        autoComplete="off"
        spellCheck={false}
      />
      <TextField
        label={t("calls.filters.since")}
        name="since"
        type="datetime-local"
        defaultValue={toLocalInput(filters.since)}
      />
      <TextField
        label={t("calls.filters.until")}
        name="until"
        type="datetime-local"
        defaultValue={toLocalInput(filters.until)}
      />
    </FilterBar>
  );
}

function CallsTable({ rows }: { rows: AdminSchemas["AdminCallSummary"][] }) {
  const { t } = useTranslation();
  const inspect = can(useSession(), "calls.inspect");
  return (
    <Table label={t("nav.calls")}>
      <Head
        cols={[
          [t("calls.col.at")],
          [t("calls.requestId")],
          [t("calls.col.account")],
          [t("calls.col.model"), "hidden md:table-cell"],
          [t("calls.col.result")],
          [t("calls.col.latency"), "hidden lg:table-cell"],
          [t("calls.col.price"), "hidden xl:table-cell"],
        ]}
        action={inspect}
      />
      <tbody>
        {rows.map((call) => (
          <CallRow key={call.id} call={call} inspect={inspect} />
        ))}
      </tbody>
    </Table>
  );
}

function CallRow({ call, inspect }: { call: AdminSchemas["AdminCallSummary"]; inspect: boolean }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <tr>
      <Td>
        {inspect ? (
          <Link to="/calls/$callId" params={{ callId: call.id }} className="text-accent hover:underline">
            <Time at={call.at} />
          </Link>
        ) : (
          <Time at={call.at} />
        )}
      </Td>
      <Td>
        <Code>{call.id ?? "—"}</Code>
      </Td>
      <Td className={mainCell}>
        <span className="block truncate font-mono text-label text-t2" title={call.account_id}>
          {call.account_id ?? "—"}
        </span>
      </Td>
      <Td className="hidden max-w-44 md:table-cell">
        <span className="block truncate" title={call.model}>
          {call.model ?? "—"}
        </span>
      </Td>
      <Td>
        <State tone={call.error_kind ? "bad" : "good"}>{call.error_kind ? t("calls.failed") : t("calls.ok")}</State>
      </Td>
      <Td className="hidden tabular-nums lg:table-cell">
        {call.latency_ms === undefined ? "—" : t("calls.ms", { ms: format.number(call.latency_ms) })}
      </Td>
      <Td className="hidden tabular-nums xl:table-cell">{call.price ? format.money(call.price) : "—"}</Td>
      {inspect ? (
        <Td className="w-0">
          <Button asChild size="sm" variant="ghost">
            <Link to="/calls/$callId" params={{ callId: call.id }}>
              {t("calls.trace")}
              <Icon name="right" />
            </Link>
          </Button>
        </Td>
      ) : null}
    </tr>
  );
}
