import type { AdminSchemas } from "@axiom/api";
import { can, LoadMore, useSession } from "@axiom/app";
import {
  ActionButton,
  BarChart,
  Card,
  DataState,
  DataTable,
  EmptyState,
  FactList,
  Grid,
  Metric,
  PageHead,
  PageTabs,
  SegmentedControl,
  Stack,
  Tag,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link, useLocation, useNavigate, useRouter } from "@tanstack/react-router";
import { type ComponentProps, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useCallColumns } from "../call-columns";
import { CallDrawer } from "../call-drawer";
import { pluginStatsQuery } from "../content-queries";
import { FileParserSettings } from "../file-parser-settings";
import { overviewWindow } from "../overview";
import { usePanelSteps } from "../panel-steps";
import { PageBody } from "../parts";
import { type AdminPlugin, adminPlugins, notBuilt } from "../plugins";
import { adminCallsQuery } from "../queries";
import { SearchSettings } from "../search-settings";

type Stat = AdminSchemas["PluginStat"];
type Call = AdminSchemas["AdminCallSummary"];
type Run = NonNullable<Call["plugins"]>[number];

// The plugins page has three tabs, each its own address: the overview, and the two settings the plugins are set up from
// (the search engines and the file parser, which were sections of the settings page before).
const tabs = [
  { key: "overview", to: "/plugins" },
  { key: "web", to: "/plugins/web" },
  { key: "file", to: "/plugins/file" },
] as const;

const settingsOf = { web: "/plugins/web", "file-parser": "/plugins/file" } as const;

export default function Plugins() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const router = useRouter();
  const pathname = useLocation({ select: (location) => location.pathname });
  const tab = tabs.find((item) => pathname.endsWith(item.to)) ?? tabs[0];
  return (
    <PageBody>
      <PageHead title={t("shell.nav.plugins")} lede={t(`plugins.lede.${tab.key}`)} />
      <PageTabs
        label={t("plugins.tabsLabel")}
        items={tabs.map((item) => ({
          label: t(`plugins.tabs.${item.key}`),
          current: item.key === tab.key,
          href: router.buildLocation({ to: item.to }).href,
          onSelect: () => void navigate({ to: item.to }),
        }))}
      />
      {tab.key === "web" ? <SearchSettings /> : tab.key === "file" ? <FileParserSettings /> : <Overview />}
    </PageBody>
  );
}

// How the plugins were used over the last 24 hours, and the requests that used one. The window is fixed when the page
// opens, so the cards and the list describe the same hours. A part the node cannot answer yet (501) is left out
// without a word; when both are, the page says what it says before any request has used a plugin.
function Overview() {
  const { t } = useTranslation();
  const [anchor] = useState(() => new Date());
  const window = useMemo(() => overviewWindow("24h", anchor), [anchor]);
  const stats = useQuery(pluginStatsQuery(window.since, window.until));
  const canSearch = can(useSession(), "calls.search");
  const hasStats = !notBuilt(stats.error);
  return (
    <>
      {hasStats ? (
        <DataState query={stats}>
          {(data) => (
            <Grid columns={2}>
              {adminPlugins
                .flatMap((plugin) => data.plugins.find((stat) => stat.plugin === plugin) ?? [])
                .map((stat) => (
                  <PluginCard key={stat.plugin} stat={stat} />
                ))}
            </Grid>
          )}
        </DataState>
      ) : null}
      {canSearch ? <Recent /> : null}
      {!hasStats && !canSearch ? (
        <EmptyState title={t("plugins.noEmpty.title")}>{t("plugins.noEmpty.body")}</EmptyState>
      ) : null}
    </>
  );
}

// One plugin: how many requests used it, hour by hour, how many of its runs failed and how long it took. What the
// plugin's backends cost the platform is not here, since the stats do not answer it.
function PluginCard({ stat }: { stat: Stat }) {
  const { t } = useTranslation();
  const format = useFormat();
  const name = t(`calls.plugin.${stat.plugin}`);
  const points = stat.hours.map((hour) => ({ at: new Date(hour.at), values: { calls: hour.calls } }));
  return (
    <Card
      title={name}
      lede={t(`plugins.what.${stat.plugin}`)}
      actions={
        <ActionButton asChild size="sm">
          <Link to={settingsOf[stat.plugin]}>{t("plugins.settings")}</Link>
        </ActionButton>
      }
    >
      <Stack>
        <Metric bare label={t("plugins.requests")} value={stat.calls} />
        <BarChart
          label={`${name}: ${t("plugins.hourly")}`}
          empty={t("plugins.noHours")}
          series={[{ key: "calls", label: t("plugins.requests"), colour: "data" }]}
          points={points}
          unit="count"
          step="hour"
          current={points.length - 1}
          zero
        />
        <FactList
          rows={[
            [
              t("plugins.failures"),
              stat.calls > 0
                ? `${t("plugins.failureCount", { count: stat.failures })} · ${format.decimal(((stat.failures / stat.calls) * 100).toFixed(2))}%`
                : "—",
            ],
            [
              t("plugins.meanTime"),
              stat.mean_ms === undefined ? "—" : t("calls.ms", { ms: format.number(stat.mean_ms) }),
            ],
            [t(`plugins.uses.${stat.plugin}`), format.number(stat.uses)],
          ]}
        />
      </Stack>
    </Card>
  );
}

// The requests that used a plugin, newest first, through the calls list's plugin filter; a row opens the request.
function Recent() {
  const { t } = useTranslation();
  // The list has no "any plugin" filter, so it is one plugin at a time; the web search is the one most requests use.
  const [plugin, setPlugin] = useState<AdminPlugin>("web");
  const calls = useInfiniteQuery(adminCallsQuery({ plugin }));
  const rows = calls.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  const [selected, select] = useState<string>();
  const open = rows.find((call) => call.id === selected);
  const steps = usePanelSteps(rows, selected ?? "", (call) => call.id, select);
  if (notBuilt(calls.error)) return null;
  return (
    <>
      <SegmentedControl
        label={t("plugins.filterLabel")}
        value={plugin}
        onChange={setPlugin}
        options={adminPlugins.map((item) => ({ value: item, label: t(`calls.plugin.${item}`) }))}
      />
      <Card title={t("plugins.recent")} layout="flush">
        <DataState
          query={calls}
          isEmpty={() => rows.length === 0}
          empty={
            <EmptyState
              title={t("plugins.noEmpty.title")}
              action={
                <ActionButton asChild>
                  <Link to={settingsOf[plugin]}>{t("plugins.settings")}</Link>
                </ActionButton>
              }
            >
              {t("plugins.noEmpty.body")}
            </EmptyState>
          }
        >
          {() => <RecentTable rows={rows} current={selected} onOpen={(call) => select(call.id)} />}
        </DataState>
        <LoadMore list={calls} />
      </Card>
      {open ? <CallDrawer call={open} steps={steps} onClose={() => select(undefined)} /> : null}
    </>
  );
}

// A request may have used both plugins: each of its columns that belongs to a run lists one line per run.
function Runs({ call, cell }: { call: Call; cell: (run: Run) => string | undefined }) {
  const runs = call.plugins ?? [];
  if (runs.length === 0) return "—";
  return runs.map((run) => <div key={run.plugin}>{cell(run) ?? "—"}</div>);
}

function RecentTable({
  rows,
  current,
  onOpen,
}: {
  rows: Call[];
  current: string | undefined;
  onOpen: (call: Call) => void;
}) {
  const { t } = useTranslation();
  const { at, account, model } = useCallColumns();
  const { plugin, how, time, fee } = useRunColumns();
  return (
    <DataTable<Call>
      label={t("plugins.recentLabel")}
      rows={rows}
      rowKey={(call) => call.id ?? ""}
      onOpen={onOpen}
      current={current}
      columns={[at, plugin, how, account, { ...model, from: "lg" }, time, fee]}
    />
  );
}

type Column = ComponentProps<typeof DataTable<Call>>["columns"][number];

// What each plugin the request used did: its name, how it handled the request, how long it took and what it charged.
function useRunColumns(): Record<"plugin" | "how" | "time" | "fee", Column> {
  const { t } = useTranslation();
  const format = useFormat();
  return {
    plugin: {
      key: "plugin",
      header: t("plugins.col.plugin"),
      cell: (call) =>
        (call.plugins ?? []).map((run) => (
          <div key={run.plugin}>
            {t(`calls.plugin.${run.plugin}`)} {run.ok ? null : <Tag tone="bad">{t("calls.failed")}</Tag>}
          </div>
        )),
    },
    how: {
      key: "how",
      header: t("plugins.col.how"),
      from: "md",
      cell: (call) => (
        <Runs
          call={call}
          cell={(run) =>
            run.engine
              ? t(run.plugin === "web" ? "plugins.search" : "plugins.parse", {
                  engine: run.engine,
                  count: run.uses ?? 0,
                })
              : undefined
          }
        />
      ),
    },
    time: {
      key: "time",
      header: t("plugins.col.time"),
      align: "end",
      from: "md",
      cell: (call) => (
        <Runs
          call={call}
          cell={(run) => (run.ms === undefined ? undefined : t("calls.ms", { ms: format.number(run.ms) }))}
        />
      ),
    },
    fee: {
      key: "fee",
      header: t("plugins.col.fee"),
      align: "end",
      from: "sm",
      cell: (call) => <Runs call={call} cell={(run) => (run.price ? format.money(run.price) : undefined)} />,
    },
  };
}

export { default as messages } from "../locales/plugins";
