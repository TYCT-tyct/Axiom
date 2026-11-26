import { accountKey } from "@axiom/app";
import {
  ActionButton,
  Cluster,
  DataState,
  EmptyState,
  Grid,
  PageHead,
  SegmentedControl,
  Text,
  useFormat,
} from "@axiom/ui";
import { useIsFetching, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { LinesCard } from "../lines-card";
import enMonitor from "../locales/en/monitor";
import enOps from "../locales/en/ops";
import zhMonitor from "../locales/zh/monitor";
import zhOps from "../locales/zh/ops";
import type { Monitor } from "../monitor";
import { CausesCard, FailedCard, FirstTokenCard, RequestsCard, SpreadCard } from "../monitor-cards";
import { Figures } from "../monitor-figures";
import { HealthCard } from "../monitor-health";
import { type MonitorWindow, monitorQuery, notBuilt } from "../monitor-queries";
import { MonitorTabs } from "../monitor-tabs";
import { PageBody } from "../parts";
import { BalancesCard } from "../upstream-balances";

export const namespaces = {
  monitor: { zh: zhMonitor, en: enMonitor },
  ops: { zh: zhOps, en: enOps },
};

const windows: readonly MonitorWindow[] = ["1h", "24h"];

// The page's controls: when the window was read, a button to read it again, and the window itself.
function Controls({ window, readAt }: { window: MonitorWindow; readAt: number }) {
  const { t } = useTranslation("monitor");
  const format = useFormat();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const fetching = useIsFetching({ queryKey: [accountKey, "operations-monitor"] }) > 0;
  return (
    <Cluster gap={8}>
      {readAt ? (
        <Text as="span" size="sm" tone="faint" numeric>
          {t("updated", { time: format.hour(new Date(readAt)) })}
        </Text>
      ) : null}
      <ActionButton
        variant="gho"
        size="sm"
        icon="refresh"
        aria-label={t("refresh")}
        pending={fetching}
        onClick={() => void queryClient.invalidateQueries({ queryKey: [accountKey] })}
      />
      <SegmentedControl<MonitorWindow>
        label={t("windowLabel")}
        value={window}
        options={windows.map((value) => ({ value, label: t(`window.${value}`) }))}
        onChange={(value) => navigate({ to: "/monitor", search: { window: value }, replace: true })}
      />
    </Cluster>
  );
}

function Overview({ monitor }: { monitor: Monitor }) {
  return (
    <>
      <Grid columns="1:1">
        <HealthCard monitor={monitor} />
        <Figures monitor={monitor} />
      </Grid>
      <Grid columns="1:1">
        <RequestsCard monitor={monitor} />
        <FirstTokenCard monitor={monitor} />
      </Grid>
      <Grid columns="2:1">
        <LinesCard monitor={monitor} detailed />
        <CausesCard monitor={monitor} />
      </Grid>
      <Grid columns="1:1:1">
        <FailedCard monitor={monitor} />
        <SpreadCard monitor={monitor} />
        <BalancesCard />
      </Grid>
    </>
  );
}

// 运维监控, the overview tab: the platform's health as a score with the reasons it lost points, the six numbers of the
// window, the calls and the first token over it, then the lines, why calls failed, the last that did not succeed, how long
// the completed ones took and what the suppliers have left. The window is the last hour (a point a minute) or day (a
// point an hour); the whole page is one request of the monitor, and a node that has none says so without an error.
export default function MonitorPage() {
  const { t } = useTranslation("monitor");
  const { window } = useSearch({ from: "/app/monitor" });
  const query = useQuery(monitorQuery(window));
  return (
    <PageBody>
      <PageHead title={t("title")} actions={<Controls window={window} readAt={query.dataUpdatedAt} />} />
      <MonitorTabs current="overview" />
      {notBuilt(query.error) ? (
        <EmptyState title={t("notBuilt.title")}>{t("notBuilt.body")}</EmptyState>
      ) : (
        <DataState
          query={query}
          isEmpty={(data) => data.calls === 0}
          empty={
            <EmptyState
              title={t("empty.title")}
              action={
                <ActionButton asChild>
                  <Link to="/suppliers" search={{}}>
                    {t("empty.action")}
                  </Link>
                </ActionButton>
              }
            >
              {t("empty.body")}
            </EmptyState>
          }
        >
          {(monitor) => <Overview monitor={monitor} />}
        </DataState>
      )}
    </PageBody>
  );
}
