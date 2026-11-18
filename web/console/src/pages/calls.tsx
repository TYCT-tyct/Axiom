import type { Schemas } from "@axiom/api";
import { can, identityOf, useSession } from "@axiom/app";
import LogsBtn from "@axiom/boards/AxApp/regions/LogsBtn";
import btnZh from "@axiom/boards/AxApp/regions/LogsBtn.zh";
import LogsSeg from "@axiom/boards/AxApp/regions/LogsSeg";
import segZh from "@axiom/boards/AxApp/regions/LogsSeg.zh";
import { useFormat, usePageTitle } from "@axiom/ui";
import {
  type InfiniteData,
  type UseInfiniteQueryResult,
  type UseQueryResult,
  useInfiniteQuery,
  useQuery,
} from "@tanstack/react-query";
import { useLocation, useNavigate, useRouter, useSearch } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useEffect, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { filtersUnsupported } from "../calls";
import {
  type CallFilters,
  type CallGroupBy,
  callAggregateQuery,
  callQuery,
  callSessionsQuery,
  callsQuery,
  keysQuery,
  modelsQuery,
  workspacesQuery,
} from "../queries";
import { cleanSearchFilters, dateRangeForPreset, hasActiveCallFilters, type PresetChoice } from "./calls-data";
import { CallsBoardList, InlineCallSplit } from "./calls-detail";
import { ExportNotice, ExportScopeNote, useCallsExport } from "./calls-export";
import { CallsFilterBar } from "./calls-filters";
import { buildCallListRows, callHeaders } from "./calls-rows";
import { newCallsNavId, saveCallsNavigation, takeCallsScroll } from "./calls-scroll";
import { SessionsSection, SummarySection } from "./calls-sections";
import {
  CallsAlertBanner,
  CallsCardNotice,
  CallsPageShell,
  CallsQueryState,
  segClickIndex,
  useNarrow600,
  usePhoneWidth,
} from "./calls-states";

type CallsSearch = CallFilters & { preset?: PresetChoice; group_by?: CallGroupBy; tab?: "sessions"; call?: string };

function CallsHeaderActions({
  t,
  sessionsTab,
  exportBusy,
  onTab,
  onExport,
}: {
  t: TFunction;
  sessionsTab: boolean;
  exportBusy: boolean;
  onTab: (tab: "sessions" | undefined) => void;
  onExport: () => void;
}) {
  return (
    <>
      <LogsSeg
        t={(k) => segZh[k]}
        values={{
          ariaLabel: t("callsBoard.title"),
          segStyle: `--n: 2; --i: ${sessionsTab ? 1 : 0};`,
          onClick: (e) => {
            const idx = segClickIndex(e);
            if (idx >= 0) onTab(idx === 1 ? "sessions" : undefined);
          },
        }}
        rows={{
          items0: [
            { key: "calls", kind: "0", f0: "button", f1: sessionsTab ? "" : "on", f2: t("callsBoard.tabs.calls") },
            {
              key: "sessions",
              kind: "0",
              f0: "button",
              f1: sessionsTab ? "on" : "",
              f2: t("callsBoard.tabs.sessions"),
            },
          ],
        }}
      />
      <LogsBtn
        t={(k) => btnZh[k]}
        values={{ disabled: exportBusy, onClick: onExport }}
        text={{ label: exportBusy ? t("callsBoard.export.busy") : t("callsBoard.export.action") }}
      />
    </>
  );
}

function isPlainLeftClick(e: React.SyntheticEvent) {
  const native = e.nativeEvent;
  if (!(native instanceof MouseEvent)) return true;
  return native.button === 0 && !native.metaKey && !native.ctrlKey && !native.shiftKey && !native.altKey;
}

function handleCallRowClick(
  e: React.SyntheticEvent,
  selectedId: string | undefined,
  onSelectCall: (id?: string) => void,
  onOpenCall: (id: string) => void,
) {
  const id = (e.target as Element).closest('[data-a="lrow"]')?.getAttribute("data-id");
  if (!id) return;
  if ((e.target as Element).closest("a.lgt")) {
    if (!isPlainLeftClick(e)) return;
    e.preventDefault();
    onOpenCall(id);
    return;
  }
  onSelectCall(id === selectedId ? undefined : id);
}

type CallsListBodyProps = {
  t: TFunction;
  locale: string;
  phone: boolean;
  calls: UseInfiniteQueryResult<InfiniteData<Schemas["CallList"]>, Error>;
  callRows: Schemas["Call"][];
  selectedId?: string;
  selectedQuery: UseQueryResult<Schemas["Call"], Error>;
  keyNames: Map<string, string>;
  filtered: boolean;
  onClear: () => void;
  onSelectCall: (id?: string) => void;
  onOpenCall: (id: string) => void;
};

function CallsListTable(p: CallsListBodyProps) {
  const router = useRouter();
  const format = useFormat();
  const headers = callHeaders(p.t);
  const hrefOf = (callId: string) => router.buildLocation({ to: "/calls/$callId", params: { callId } }).href;
  const rows = buildCallListRows(p.callRows, p.selectedId, p.keyNames, format, p.locale, p.t, hrefOf);
  const idx = p.selectedId ? p.callRows.findIndex((c) => c.id === p.selectedId) : -1;
  const listProps = {
    headers,
    rows,
    moreLabel: p.calls.isFetchingNextPage ? p.t("callsBoard.list.loadingMore") : p.t("callsBoard.list.loadMore"),
    hideMore: !p.calls.hasNextPage,
    moreDisabled: p.calls.isFetchingNextPage,
    onMore: () => void p.calls.fetchNextPage(),
    onListClick: (e: React.SyntheticEvent) => handleCallRowClick(e, p.selectedId, p.onSelectCall, p.onOpenCall),
    t: p.t,
  };
  if (p.selectedId) {
    return (
      <InlineCallSplit
        {...listProps}
        phone={p.phone}
        selectedCall={idx >= 0 ? p.callRows[idx] : undefined}
        selectedQuery={p.selectedQuery}
        keyNames={p.keyNames}
        hasPrev={idx > 0}
        hasNext={idx >= 0 && idx < p.callRows.length - 1}
        onPrev={() => idx > 0 && p.onSelectCall(p.callRows[idx - 1]?.id)}
        onNext={() => idx >= 0 && idx < p.callRows.length - 1 && p.onSelectCall(p.callRows[idx + 1]?.id)}
        onClose={() => p.onSelectCall(undefined)}
      />
    );
  }
  return <CallsBoardList {...listProps} />;
}

function CallsListBody(p: CallsListBodyProps) {
  if (p.calls.isPending || p.calls.isError) {
    return (
      <CallsQueryState
        t={p.t}
        error={p.calls.error}
        isPending={p.calls.isPending}
        onRetry={() => void p.calls.refetch()}
      />
    );
  }
  if (p.callRows.length === 0) {
    return (
      <CallsCardNotice
        message={p.filtered ? p.t("callsBoard.list.emptyFiltered") : p.t("callsBoard.list.emptyTitle")}
        actionLabel={p.filtered ? p.t("callsBoard.filter.clear") : undefined}
        onAction={p.filtered ? p.onClear : undefined}
      />
    );
  }
  return <CallsListTable {...p} />;
}

function useCallsPageQueries(
  search: CallsSearch,
  searchStr: string,
  restoreId: string | undefined,
  identity: string,
  canKeys: boolean,
) {
  const router = useRouter();
  const { group_by, tab, call: selectedId, preset: _preset, ...filters } = search;
  const groupBy: CallGroupBy = group_by ?? "day";
  const sessionsTab = tab === "sessions";
  const calls = useInfiniteQuery({ ...callsQuery(filters), enabled: !sessionsTab });
  const sessions = useInfiniteQuery({ ...callSessionsQuery(filters), enabled: sessionsTab });
  const report = useQuery({ ...callAggregateQuery(filters, groupBy), enabled: !sessionsTab });
  const selectedQuery = useQuery({ ...callQuery(selectedId ?? ""), enabled: Boolean(selectedId) });
  const models = useQuery({ ...modelsQuery({}), enabled: identity !== "" });
  const keys = useInfiniteQuery({ ...keysQuery({}), enabled: canKeys });
  const workspaces = useQuery({ ...workspacesQuery, enabled: canKeys });
  const exported = useCallsExport(filters);
  const callRows = useMemo(() => calls.data?.pages.flatMap((p) => p.data) ?? [], [calls.data]);
  const keyList = useMemo(() => keys.data?.pages.flatMap((p) => p.data) ?? [], [keys.data]);
  const keyNames = useMemo(() => new Map(keyList.map((k) => [k.id, k.name || k.id])), [keyList]);
  useEffect(() => {
    const ready = sessionsTab ? sessions.isSuccess : calls.isSuccess;
    if (!ready) return;
    const saved = takeCallsScroll(restoreId, identity, searchStr);
    if (saved === null || saved <= 0) return;
    let cancelled = false;
    const restore = () => {
      requestAnimationFrame(() => {
        if (!cancelled) {
          requestAnimationFrame(() => {
            if (!cancelled) window.scrollTo(0, saved);
          });
        }
      });
    };
    restore();
    const unsub = router.subscribe("onRendered", restore);
    const timer = setTimeout(unsub, 500);
    return () => {
      cancelled = true;
      clearTimeout(timer);
      unsub();
    };
  }, [calls.isSuccess, sessions.isSuccess, sessionsTab, restoreId, identity, searchStr, router]);
  return {
    filters,
    group_by,
    groupBy,
    tab,
    sessionsTab,
    selectedId,
    calls,
    sessions,
    report,
    selectedQuery,
    models,
    keys,
    keyList,
    keyNames,
    workspaces,
    exported,
    callRows,
  };
}

function CallsResults({
  t,
  locale,
  phone,
  narrow,
  q,
  setSearch,
  clearAll,
  onOpenCall,
}: {
  t: TFunction;
  locale: string;
  phone: boolean;
  narrow: boolean;
  q: ReturnType<typeof useCallsPageQueries>;
  setSearch: (patch: Partial<CallsSearch>, replace?: boolean) => void;
  clearAll: () => void;
  onOpenCall: (callId: string) => void;
}) {
  const unsupported = !q.sessionsTab && filtersUnsupported(q.filters, q.calls.data?.pages ?? []);
  const filtered = hasActiveCallFilters(q.filters);
  const unsupportedName = q.filters.fell_back ? t("callsBoard.filter.statusFallback") : t("callsBoard.filter.status");
  return (
    <>
      {!q.sessionsTab ? (
        <SummarySection
          t={t}
          narrow={narrow}
          report={q.report}
          loadedCalls={q.callRows}
          loadedReady={q.calls.isSuccess}
          groupBy={q.groupBy}
          fellBackActive={Boolean(q.filters.fell_back)}
          keyNames={q.keyNames}
          onGroupBy={(next) => setSearch({ group_by: next })}
        />
      ) : null}
      {unsupported ? (
        <CallsAlertBanner
          lead={t("callsBoard.list.unsupportedTitle")}
          body={t("callsBoard.list.unsupportedBody", { filter: unsupportedName })}
          actionLabel={t("callsBoard.filter.clear")}
          onAction={clearAll}
        />
      ) : null}
      {q.sessionsTab ? (
        <SessionsSection t={t} narrow={narrow} sessions={q.sessions} filtered={filtered} onClear={clearAll} />
      ) : (
        <CallsListBody
          t={t}
          locale={locale}
          phone={phone}
          calls={q.calls}
          callRows={q.callRows}
          selectedId={q.selectedId}
          selectedQuery={q.selectedQuery}
          keyNames={q.keyNames}
          filtered={filtered}
          onClear={clearAll}
          onSelectCall={(call) => setSearch({ call }, true)}
          onOpenCall={onOpenCall}
        />
      )}
    </>
  );
}

function openCallWithReturn(
  navigate: ReturnType<typeof useNavigate>,
  identity: string,
  search: CallsSearch,
  searchStr: string,
  callId: string,
) {
  const navId = newCallsNavId();
  saveCallsNavigation(navId, identity, search, searchStr, window.scrollY);
  void navigate({
    to: "/calls/$callId",
    params: { callId },
    state: ((prev: unknown) => ({ ...(typeof prev === "object" && prev ? prev : {}), callsFrom: navId })) as never,
  });
}

function CallsPageContent({
  t,
  locale,
  phone,
  narrow,
  canKeys,
  preset,
  q,
  setSearch,
  clearAll,
  onOpenCall,
}: {
  t: TFunction;
  locale: string;
  phone: boolean;
  narrow: boolean;
  canKeys: boolean;
  preset?: PresetChoice;
  q: ReturnType<typeof useCallsPageQueries>;
  setSearch: (patch: Partial<CallsSearch>, replace?: boolean) => void;
  clearAll: () => void;
  onOpenCall: (callId: string) => void;
}) {
  return (
    <CallsPageShell
      phone={phone}
      heading={t("callsBoard.title")}
      headerActions={
        <CallsHeaderActions
          t={t}
          sessionsTab={q.sessionsTab}
          exportBusy={q.exported.busy}
          onTab={(next) => setSearch({ tab: next, call: undefined })}
          onExport={q.exported.start}
        />
      }
    >
      <ExportNotice
        t={t}
        error={q.exported.failed}
        filters={q.filters}
        onUse7d={() => {
          q.exported.dismiss();
          setSearch({ ...dateRangeForPreset("d7"), preset: "d7", call: undefined });
        }}
        onDismiss={q.exported.dismiss}
      />
      <ExportScopeNote t={t} filters={q.filters} />
      <CallsFilterBar
        t={t}
        filters={q.filters}
        preset={preset}
        canKeys={canKeys}
        hasMoreKeys={Boolean(q.keys.hasNextPage)}
        onLoadMoreKeys={() => void q.keys.fetchNextPage()}
        models={q.models.data?.data ?? []}
        keys={q.keyList}
        workspaces={q.workspaces.data?.data ?? []}
        onApply={(next) => setSearch({ ...next, group_by: q.group_by, tab: q.tab, call: undefined })}
      />
      <CallsResults
        t={t}
        locale={locale}
        phone={phone}
        narrow={narrow}
        q={q}
        setSearch={setSearch}
        clearAll={clearAll}
        onOpenCall={onOpenCall}
      />
    </CallsPageShell>
  );
}

export default function Calls() {
  const { t, i18n } = useTranslation();
  const session = useSession();
  const identity = identityOf(session);
  const canKeys = can(session, "api_keys.view");
  const phone = usePhoneWidth();
  const narrow = useNarrow600();
  const navigate = useNavigate();
  const searchStr = useLocation({ select: (l) => l.searchStr });
  const restoreId = useLocation({ select: (l) => (l.state as { callsRestore?: string } | undefined)?.callsRestore });
  const search: CallsSearch = useSearch({ from: "/app/calls" });
  const q = useCallsPageQueries(search, searchStr, restoreId, identity, canKeys);
  usePageTitle(t("callsBoard.title"));
  const setSearch = (patch: Partial<CallsSearch>, replace = false) =>
    void navigate({ to: "/calls", search: cleanSearchFilters({ ...search, ...patch }), replace, resetScroll: false });
  const clearAll = () =>
    void navigate({ to: "/calls", search: cleanSearchFilters({ tab: q.tab }), resetScroll: false });
  return (
    <CallsPageContent
      t={t}
      locale={i18n.resolvedLanguage ?? i18n.language}
      phone={phone}
      narrow={narrow}
      canKeys={canKeys}
      preset={search.preset}
      q={q}
      setSearch={setSearch}
      clearAll={clearAll}
      onOpenCall={(callId) => openCallWithReturn(navigate, identity, search, searchStr, callId)}
    />
  );
}
