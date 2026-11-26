import { accountKey, can, useSession } from "@axiom/app";
import {
  ActionButton,
  Cluster,
  DataState,
  EmptyState,
  LoadingState,
  Notice,
  PageHead,
  SegmentedControl,
  Text,
  useFormat,
} from "@axiom/ui";
import { keepPreviousData, useIsFetching, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, Navigate, useNavigate, useSearch } from "@tanstack/react-router";
import { lazy, Suspense, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Overview } from "../dashboard";
import { Figures } from "../dashboard-figures";
import enDashboard from "../locales/en/dashboard";
import { loadTranslatedPage } from "../locales/load-page";
import zhDashboard from "../locales/zh/dashboard";
import { pages } from "../nav";
import { type OverviewRange, overviewRanges, overviewWindows } from "../overview";
import { PageBody } from "../parts";
import { overviewQuery } from "../queries";

export const namespaces = {
  dashboard: { zh: zhDashboard, en: enDashboard },
};

// The admin's home is the operations dashboard (docs/domains/operations.md「运营看板」, the board's 仪表盘): the site's
// calls, what they earned and cost, who spent, what waits for a person, and how the month is going against its targets.
// A session without sites.manage sees the first page it may use instead.
export default function Home() {
  const session = useSession();
  if (!can(session, "sites.manage")) {
    const first = pages.find((item) => item.to !== "/" && can(session, item.permission));
    if (first) {
      return <Navigate to={first.to} replace />;
    }
    return <NoAccess />;
  }
  return <Dashboard />;
}

function NoAccess() {
  const { t } = useTranslation("dashboard");
  return (
    <PageBody>
      <PageHead title={t("noAccessTitle")} lede={t("noAccessBody")} />
    </PageBody>
  );
}

// The cards under the figures are a chunk of their own (dashboard-cards.tsx), so the first screen is the head and the
// figures.
const Cards = lazy(() => loadTranslatedPage(import("../dashboard-cards")));

// A figure the page asks for again, or for another window, stays on screen until the new one arrives.
const kept = <T extends object>(options: T) => ({ ...options, placeholderData: keepPreviousData });

// The figures the dashboard shows, for the window the range names. The visit's instant is the window's end, not the
// render's: a range change must not move the window under the figures; refreshing is the one thing that moves it, to now.
function useDashboard(range: OverviewRange) {
  const queryClient = useQueryClient();
  const [anchor, setAnchor] = useState(() => new Date());
  const windows = useMemo(() => overviewWindows(range, anchor), [range, anchor]);
  const current = useQuery(kept(overviewQuery(windows.current.since, windows.current.until)));
  const previous = useQuery(kept(overviewQuery(windows.previous.since, windows.previous.until)));
  const trend = useQuery(kept(overviewQuery(windows.trend.since, windows.trend.until)));
  const refresh = () => {
    setAnchor(new Date());
    void queryClient.invalidateQueries({ queryKey: [accountKey] });
  };
  return { anchor, windows, current, previous, trend, refresh };
}

// The page's controls: when the figures were read, a button to read them again, and the range.
function Controls({ range, anchor, refresh }: { range: OverviewRange; anchor: Date; refresh: () => void }) {
  const { t } = useTranslation("dashboard");
  const format = useFormat();
  const navigate = useNavigate();
  const fetching = useIsFetching({ queryKey: [accountKey, "overview"] }) > 0;
  return (
    <Cluster gap={8}>
      <Text as="span" size="sm" tone="faint" numeric>
        {t("updated", { time: format.hour(anchor) })}
      </Text>
      <ActionButton
        variant="gho"
        size="sm"
        icon="refresh"
        aria-label={t("refresh")}
        pending={fetching}
        onClick={refresh}
      />
      <SegmentedControl<OverviewRange>
        label={t("rangeLabel")}
        value={range}
        options={overviewRanges.map((value) => ({ value, label: t(`range.${value}`) }))}
        onChange={(value) => navigate({ to: "/", search: { range: value }, replace: true })}
      />
    </Cluster>
  );
}

function Dashboard() {
  const { t } = useTranslation("dashboard");
  const format = useFormat();
  const { range } = useSearch({ from: "/app/" });
  const { anchor, windows, current, previous, trend, refresh } = useDashboard(range);
  return (
    <PageBody>
      <PageHead
        title={t("title")}
        lede={t("window", {
          since: format.dateTime(new Date(windows.current.since)),
          until: format.dateTime(new Date(windows.current.until)),
        })}
        actions={<Controls range={range} anchor={anchor} refresh={refresh} />}
      />
      <DataState
        query={current}
        isEmpty={(data) => data.calls === 0 && (trend.data?.calls ?? 0) === 0 && !data.top_models?.length}
        empty={
          <EmptyState title={t("empty.title")} action={emptyAction(t("empty.action"))}>
            {t("empty.body")}
          </EmptyState>
        }
      >
        {(data) => (
          <>
            <Coverage data={data} />
            <Figures
              range={range}
              window={windows.current}
              current={data}
              previous={previous.data}
              trend={trend.data}
            />
            <Suspense fallback={<LoadingState />}>
              <Cards overview={data} trend={trend.data ?? data} range={range} anchor={anchor} />
            </Suspense>
          </>
        )}
      </DataState>
    </PageBody>
  );
}

function emptyAction(label: string) {
  return (
    <ActionButton asChild variant="pri">
      <Link to="/suppliers" search={{}}>
        {label}
      </Link>
    </ActionButton>
  );
}

// Coverage says what the figures leave out: calls the log no longer holds, and the cost of calls whose cost is unknown,
// which the margin therefore overstates.
function Coverage({ data }: { data: Overview }) {
  const { t } = useTranslation("dashboard");
  const format = useFormat();
  return (
    <>
      {data.calls_incomplete === undefined ? (
        <Notice>{t("coverage.unavailable")}</Notice>
      ) : data.calls_incomplete ? (
        <Notice>
          {data.calls_available_since
            ? t("coverage.since", { since: format.dateTime(new Date(data.calls_available_since)) })
            : t("coverage.unknown")}
        </Notice>
      ) : null}
      {data.unknown_cost_calls ? (
        <Notice>{t("coverage.cost", { calls: format.number(data.unknown_cost_calls) })}</Notice>
      ) : null}
    </>
  );
}
