import type { AdminSchemas } from "@axiom/api";
import { can, LoadMore, useSession } from "@axiom/app";
import {
  Card,
  DataState,
  DataTable,
  EmptyState,
  Field,
  MetricStrip,
  Notice,
  PageHead,
  type StripItem,
  Tag,
  TextLink,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useCallColumns } from "../call-columns";
import { CallDrawer } from "../call-drawer";
import en from "../locales/en/calls";
import zh from "../locales/zh/calls";
import type { CallsEmptyKind } from "../overview";
import { callStatus, callsArchivedWhole, callsCoverage, callsEmptyKind, overviewWindow } from "../overview";
import { usePanelSteps } from "../panel-steps";
import { ChoiceFilter, FilterBar, PageBody, State, text, toInstant, toLocalInput } from "../parts";
import { adminPlugins, callPlugin } from "../plugins";
import { adminCallsQuery, overviewQuery } from "../queries";

export const messages = { zh: { calls: zh }, en: { calls: en } };

type Call = AdminSchemas["AdminCallSummary"];

type CallFilters = {
  account_id?: string;
  request_id?: string;
  site_id?: string;
  model?: string;
  status?: "ok" | "error";
  plugin?: AdminSchemas["AdminPlugin"];
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
      filters.plugin ||
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
    plugin: callPlugin(values.plugin),
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

// The list of calls and, over it, one call's detail (adPg_calls in admin_pages_c.js). Whoever may search calls reads the
// summary of each; the lines a request went through and what it cost upstream are the operator's, and the drawer asks
// for them only when the session may inspect.
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
      plugin: filters.plugin,
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
  const [selected, select] = useState<string>();
  const open = rows.find((call) => call.id === selected);
  const steps = usePanelSteps(rows, selected ?? "", (call) => call.id, select);
  return (
    <>
      <PageBody>
        <PageHead title={t("shell.nav.calls")} lede={t("calls.description")} />
        <Strip />
        <Filters filters={filters} />
        {filters.site_id ? (
          <Notice>
            {t("calls.filters.siteScoped")}{" "}
            <TextLink>
              <Link to="/calls" search={{ ...filters, site_id: undefined }}>
                {t("calls.filters.allSites")}
              </Link>
            </TextLink>
          </Notice>
        ) : null}
        <Card layout="flush">
          {unsupported ? <Notice>{t("calls.filters.unsupported")}</Notice> : null}
          <CoverageNotice coverage={coverage} availableSince={availableSince} />
          <DataState query={calls} isEmpty={() => rows.length === 0} empty={<CallEmpty kind={emptyKind} />}>
            {() => <CallsTable rows={rows} current={selected} onOpen={(call) => select(call.id)} />}
          </DataState>
          <LoadMore list={calls} />
        </Card>
      </PageBody>
      {open ? <CallDrawer call={open} steps={steps} onClose={() => select(undefined)} /> : null}
    </>
  );
}

// The four numbers over the list, from the dashboard's one request for the last 24 hours, so each reads the same as
// there. A session that may not read the dashboard, a node that has not got it and a number the node does not give
// leave their place out; nothing here is made up.
function Strip() {
  const { t } = useTranslation();
  const format = useFormat();
  const allowed = can(useSession(), "sites.manage");
  const [anchor] = useState(() => new Date());
  const window = useMemo(() => overviewWindow("24h", anchor), [anchor]);
  const overview = useQuery({ ...overviewQuery(window.since, window.until), enabled: allowed });
  const data = overview.data;
  if (!data) return null;
  const items: StripItem[] = [];
  if (data.calls !== undefined) {
    items.push({
      key: "requests",
      label: t("calls.strip.requests"),
      value: format.number(data.calls),
      note: data.calls_incomplete ? t("calls.strip.incomplete") : t("calls.strip.requestsNote"),
    });
    if (data.errors !== undefined && data.calls > 0) {
      items.push({
        key: "failRate",
        label: t("calls.strip.failRate"),
        value: format.decimal(((data.errors / data.calls) * 100).toFixed(2)),
        suffix: "%",
        note: t("calls.strip.failRateNote", { count: data.errors }),
        link: <Link to="/calls" search={{ status: "error" }} />,
      });
    }
  }
  if (data.first_token_ms_p50 !== undefined) {
    items.push({
      key: "firstToken",
      label: t("calls.strip.firstToken"),
      value: format.decimal((data.first_token_ms_p50 / 1000).toFixed(2)),
      suffix: t("calls.strip.seconds"),
      note: t("calls.strip.firstTokenNote"),
    });
  }
  if (data.revenue !== undefined) {
    items.push({
      key: "revenue",
      label: t("calls.strip.revenue"),
      value: format.money(data.revenue),
      note: t("calls.strip.revenueNote"),
    });
  }
  return items.length ? <MetricStrip label={t("calls.strip.label")} items={items} /> : null;
}

function Filters({ filters }: { filters: CallFilters }) {
  const navigate = useNavigate();
  return (
    <FilterBar
      values={filters}
      onApply={(values) => navigate({ to: "/calls", search: callSearch(values, filters.site_id) })}
    >
      <Searches filters={filters} />
      <Choices filters={filters} />
      <Window filters={filters} />
    </FilterBar>
  );
}

// What a call is found by: the account, the request and the model, each exact.
function Searches({ filters }: { filters: CallFilters }) {
  const { t } = useTranslation();
  return (
    <>
      <Field
        label={t("calls.filters.account")}
        hint={t("calls.filters.accountHint")}
        name="account_id"
        defaultValue={filters.account_id}
        maxLength={64}
        autoComplete="off"
        spellCheck={false}
      />
      <Field
        label={t("calls.filters.request")}
        hint={t("calls.filters.requestHint")}
        name="request_id"
        defaultValue={filters.request_id}
        autoComplete="off"
        spellCheck={false}
      />
      <Field
        label={t("calls.filters.model")}
        hint={t("calls.filters.modelHint")}
        name="model"
        defaultValue={filters.model}
        maxLength={200}
        autoComplete="off"
        spellCheck={false}
      />
    </>
  );
}

// How it went and what it used.
function Choices({ filters }: { filters: CallFilters }) {
  const { t } = useTranslation();
  return (
    <>
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
      <ChoiceFilter
        label={t("calls.filters.plugin")}
        name="plugin"
        value={filters.plugin}
        any={t("calls.filters.anyPlugin")}
        options={adminPlugins.map((plugin) => ({ value: plugin, label: t(`calls.plugin.${plugin}`) }))}
      />
    </>
  );
}

// When it was made.
function Window({ filters }: { filters: CallFilters }) {
  const { t } = useTranslation();
  return (
    <>
      <Field
        label={t("calls.filters.since")}
        name="since"
        type="datetime-local"
        defaultValue={toLocalInput(filters.since)}
      />
      <Field
        label={t("calls.filters.until")}
        name="until"
        type="datetime-local"
        defaultValue={toLocalInput(filters.until)}
      />
    </>
  );
}

// A call's plugins, a tag for each, so the list shows which requests searched the web or read a file.
function PluginTags({ call }: { call: Call }) {
  const { t } = useTranslation();
  if (!call.plugins?.length) return "—";
  return call.plugins.map((run) => (
    <Tag key={run.plugin} tone={run.ok ? "neutral" : "bad"}>
      {t(`calls.plugin.${run.plugin}`)}
    </Tag>
  ));
}

function CallsTable({
  rows,
  current,
  onOpen,
}: {
  rows: Call[];
  current: string | undefined;
  onOpen: (call: Call) => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  const { at, id, account, model } = useCallColumns();
  return (
    <DataTable<Call>
      label={t("shell.nav.calls")}
      rows={rows}
      rowKey={(call) => call.id ?? ""}
      onOpen={onOpen}
      current={current}
      columns={[
        at,
        id,
        account,
        model,
        { key: "plugins", header: t("calls.col.plugins"), from: "xl", cell: (call) => <PluginTags call={call} /> },
        {
          key: "latency",
          header: t("calls.col.latency"),
          align: "end",
          from: "md",
          sort: (call) => call.latency_ms,
          cell: (call) => (call.latency_ms === undefined ? "—" : t("calls.ms", { ms: format.number(call.latency_ms) })),
        },
        {
          key: "price",
          header: t("calls.col.price"),
          align: "end",
          from: "sm",
          cell: (call) => (call.price ? format.money(call.price) : "—"),
        },
        {
          key: "result",
          header: t("calls.col.result"),
          cell: (call) => (
            <State tone={call.error_kind ? "bad" : "good"}>{call.error_kind ? t("calls.failed") : t("calls.ok")}</State>
          ),
        },
      ]}
    />
  );
}
