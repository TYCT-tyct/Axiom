import type { AdminSchemas } from "@axiom/api";
import { can, LoadMore, useSession } from "@axiom/app";
import {
  Button,
  DataState,
  EmptyState,
  Icon,
  Notice,
  PageHeader,
  Panel,
  Table,
  Td,
  TextField,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { callStatus } from "../overview";
import {
  ChoiceFilter,
  Code,
  FilterBar,
  Head,
  mainCell,
  PageBody,
  State,
  Time,
  text,
  toInstant,
  toLocalInput,
} from "../parts";
import { adminCallsQuery } from "../queries";

type CallFilters = {
  account_id?: string;
  request_id?: string;
  site_id?: string;
  model?: string;
  status?: "ok" | "error";
  since?: string;
  until?: string;
};

function narrowed(filters: CallFilters): boolean {
  return Boolean(
    filters.account_id ||
      filters.request_id ||
      filters.site_id ||
      filters.model ||
      filters.status ||
      filters.since ||
      filters.until,
  );
}

// An older node does not know the model and result filters: it answers without echoing them, and its rows were not
// narrowed, so the page says so instead of passing them off as the filtered result.
export function unfiltered(filters: CallFilters, page: AdminSchemas["AdminCallList"] | undefined): boolean {
  return (
    (filters.site_id !== undefined && page?.site_filter === undefined) ||
    (filters.model !== undefined && page?.model_filter === undefined) ||
    (filters.status !== undefined && page?.status_filter === undefined)
  );
}

// What the filter form means in the address: text that is blank narrows nothing, and a time is the instant it names.
function callSearch(values: Record<string, string>, site?: string) {
  return {
    account_id: text(values.account_id),
    request_id: text(values.request_id),
    site_id: site,
    model: text(values.model),
    status: callStatus(values.status),
    since: toInstant(values.since),
    until: toInstant(values.until),
  };
}

// Any loaded page without the echo means an older node answered part of this list: the model, result and site filters
// did not narrow it, so the page says so for the whole list.
export function anyUnfiltered(filters: CallFilters, pages: readonly AdminSchemas["AdminCallList"][]): boolean {
  return pages.some((page) => unfiltered(filters, page));
}

// The support view of calls: what a person asks about a bill, without costs, endpoints or routes. The operator's trace
// is a separate page and permission, so this one never shows a field the support role may not read.
export default function Calls() {
  const { t } = useTranslation();
  const filters = useSearch({ from: "/app/calls" });
  const calls = useInfiniteQuery(
    adminCallsQuery({
      account_id: filters.account_id,
      request_id: filters.request_id,
      site_id: filters.site_id,
      model: filters.model,
      status: filters.status,
      since: filters.since,
      until: filters.until,
    }),
  );
  const rows = calls.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  const unsupported = calls.data ? anyUnfiltered(filters, calls.data.pages) : false;
  return (
    <>
      <PageHeader title={t("nav.calls")} description={t("calls.description")} />
      <PageBody>
        <Filters filters={filters} />
        {filters.site_id ? (
          <Notice>
            {t("calls.filters.siteScoped")}{" "}
            <Link
              to="/calls"
              search={{ ...filters, site_id: undefined }}
              className="text-accent underline-offset-4 hover:underline"
            >
              {t("calls.filters.allSites")}
            </Link>
          </Notice>
        ) : null}
        <Panel>
          {unsupported ? <Notice>{t("calls.filters.unsupported")}</Notice> : null}
          <DataState
            query={calls}
            isEmpty={() => rows.length === 0}
            empty={
              <EmptyState title={narrowed(filters) ? t("calls.noMatch.title") : t("calls.empty.title")}>
                {narrowed(filters) ? t("calls.noMatch.body") : t("calls.empty.body")}
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

function Filters({ filters }: { filters: CallFilters }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  return (
    <FilterBar
      values={filters}
      onApply={(values) => navigate({ to: "/calls", search: callSearch(values, filters.site_id) })}
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
        label={t("calls.filters.model")}
        hint={t("calls.filters.modelHint")}
        name="model"
        defaultValue={filters.model}
        maxLength={200}
        autoComplete="off"
        spellCheck={false}
      />
      <ChoiceFilter
        label={t("calls.filters.result")}
        name="status"
        value={filters.status}
        any={t("calls.filters.anyResult")}
        options={[
          { value: "ok", label: t("calls.filters.ok") },
          { value: "error", label: t("calls.filters.error") },
        ]}
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
          [t("calls.col.at"), "hidden sm:table-cell"],
          [t("calls.requestId")],
          [t("calls.col.account")],
          [t("calls.col.model"), "hidden md:table-cell"],
          [t("calls.col.result")],
          [t("calls.col.latency"), "hidden text-right lg:table-cell"],
          [t("calls.col.price"), "hidden text-right xl:table-cell"],
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
    <tr className="hover:bg-s2">
      <Td className="hidden sm:table-cell">
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
        <span className="block text-label text-t3 sm:hidden">
          <Time at={call.at} />
        </span>
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
      <Td className="hidden whitespace-nowrap text-right tabular-nums lg:table-cell">
        {call.latency_ms === undefined ? "—" : t("calls.ms", { ms: format.number(call.latency_ms) })}
      </Td>
      <Td className="hidden whitespace-nowrap text-right tabular-nums xl:table-cell">
        {call.price ? format.money(call.price) : "—"}
      </Td>
      {inspect ? (
        <Td className="w-0">
          <Button asChild size="sm" variant="ghost">
            <Link to="/calls/$callId" params={{ callId: call.id }} aria-label={t("calls.trace")}>
              <span className="hidden sm:inline">{t("calls.trace")}</span>
              <Icon name="right" />
            </Link>
          </Button>
        </Td>
      ) : null}
    </tr>
  );
}
