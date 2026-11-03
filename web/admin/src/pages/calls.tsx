import type { AdminSchemas } from "@axiom/api";
import { can, LoadMore, useSession } from "@axiom/app";
import {
  Button,
  DataState,
  EmptyState,
  Icon,
  Id,
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
import type { CallsEmptyKind } from "../overview";
import { callStatus, callsArchivedWhole, callsCoverage, callsEmptyKind } from "../overview";
import { ChoiceFilter, FilterBar, Head, PageBody, State, Time, text, toInstant, toLocalInput } from "../parts";
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

// CallEmpty explains an empty list without calling a partly archived window archived, or claiming a filter ruled out
// calls that were archived. Its own switch keeps the page's render tree readable.
function CallEmpty({ kind }: { kind: CallsEmptyKind }) {
  const { t } = useTranslation();
  switch (kind) {
    case "archived":
      return <EmptyState title={t("calls.archived.title")}>{t("calls.archived.body")}</EmptyState>;
    case "retainedFiltered":
      return <EmptyState title={t("calls.retainedFiltered.title")}>{t("calls.retainedFiltered.body")}</EmptyState>;
    case "retainedWindow":
      return <EmptyState title={t("calls.retainedWindow.title")}>{t("calls.retainedWindow.body")}</EmptyState>;
    case "filtered":
      return <EmptyState title={t("calls.noMatch.title")}>{t("calls.noMatch.body")}</EmptyState>;
    case "window":
      return <EmptyState title={t("calls.empty.title")}>{t("calls.empty.body")}</EmptyState>;
  }
}

// CoverageNotice says how far back the log answers, and stays silent when every loaded page covers the window.
function CoverageNotice({
  coverage,
  availableSince,
}: {
  coverage: boolean | undefined;
  availableSince: string | undefined;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  if (coverage === undefined) {
    return <Notice>{t("calls.coverageUnavailable")}</Notice>;
  }
  if (!coverage) {
    return null;
  }
  return (
    <Notice>
      {availableSince
        ? t("calls.incomplete", { since: format.dateTime(new Date(availableSince)) })
        : t("calls.incompleteUnknown")}
    </Notice>
  );
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
  const page = calls.data?.pages[0];
  const coverage = calls.data ? callsCoverage(calls.data.pages) : false;
  const availableSince = page?.available_since;
  const emptyKind = callsEmptyKind(
    coverage,
    callsArchivedWhole(coverage, availableSince, page?.until),
    narrowed(filters),
  );
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
          <CoverageNotice coverage={coverage} availableSince={availableSince} />
          <DataState query={calls} isEmpty={() => rows.length === 0} empty={<CallEmpty kind={emptyKind} />}>
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
    <Table label={t("nav.calls")} fixed>
      <Head
        cols={[
          [t("calls.col.at"), "hidden w-40 sm:table-cell"],
          [t("calls.requestId"), "w-[45%] sm:w-[22%]"],
          [t("calls.col.account"), "hidden w-[14%] sm:table-cell"],
          [t("calls.col.model"), "hidden w-36 md:table-cell"],
          [t("calls.col.result"), "w-16"],
          [t("calls.col.latency"), "hidden w-28 text-right lg:table-cell"],
          [t("calls.col.price"), "hidden w-20 text-right xl:table-cell"],
        ]}
        action={inspect}
        actionClass="w-20 sm:w-28"
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
        {call.id ? <Id value={call.id} /> : "—"}
        <span className="block text-label text-t3 sm:hidden">
          <Time at={call.at} />
        </span>
      </Td>
      <Td className="hidden sm:table-cell">{call.account_id ? <Id value={call.account_id} /> : "—"}</Td>
      <Td className="hidden md:table-cell">
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
        <Td>
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
