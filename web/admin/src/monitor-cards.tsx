import { can, useSession } from "@axiom/app";
import { ActionButton, BarChart, BarRows, Card, LineChart, RowList, Text, useFormat } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { type Monitor, ranges } from "./monitor";
import { callsDrill } from "./overview";
import { adminCallsQuery } from "./queries";

// RequestsCard is the calls of each minute or hour, the part that failed in red at the bottom (the board's 请求量).
export function RequestsCard({ monitor }: { monitor: Monitor }) {
  const { t } = useTranslation("monitor");
  const label = t("requests.title");
  return (
    <Card title={label} lede={t(`requests.per.${monitor.window}`)}>
      <BarChart
        label={label}
        empty={t("requests.empty")}
        series={[
          { key: "failed", label: t("requests.failed"), colour: "bad" },
          { key: "ok", label: t("requests.ok"), colour: "data" },
        ]}
        points={monitor.buckets.map((bucket) => ({
          at: new Date(bucket.at),
          values: { failed: bucket.failures, ok: Math.max(0, bucket.calls - bucket.failures) },
        }))}
        unit="count"
        step="hour"
        zero
      />
    </Card>
  );
}

// FirstTokenCard is how long the first token took in each minute or hour, the median and the 95th percentile (the
// board's 首字时间), in milliseconds as the API counts them.
export function FirstTokenCard({ monitor }: { monitor: Monitor }) {
  const { t } = useTranslation("monitor");
  const label = t("firstTokenChart.title");
  return (
    <Card title={label} lede={t("firstTokenChart.unit")}>
      <LineChart
        label={label}
        empty={t("requests.empty")}
        series={[
          { key: "p95", label: t("firstTokenChart.p95"), colour: "data3" },
          { key: "p50", label: t("firstTokenChart.p50"), colour: "data" },
        ]}
        points={monitor.buckets.map((bucket) => ({
          at: new Date(bucket.at),
          values: { p50: bucket.first_token_ms_p50 ?? 0, p95: bucket.first_token_ms_p95 ?? 0 },
        }))}
        unit="count"
        step="hour"
        zero
      />
    </Card>
  );
}

// The failure kinds the platform names itself; any other is shown as the call log wrote it.
const known = ["upstream_error", "timeout", "no_endpoint"] as const;
const named = (kind: string): kind is (typeof known)[number] => (known as readonly string[]).includes(kind);

// CausesCard is why calls failed, most first: the supplier's side in red, the request's own in grey (the board's 失败原因).
export function CausesCard({ monitor }: { monitor: Monitor }) {
  const { t } = useTranslation("monitor");
  const format = useFormat();
  return (
    <Card title={t("causes.title")} lede={t("causes.total", { n: format.number(monitor.failures) })}>
      {monitor.failure_causes.length === 0 ? (
        <Text tone="faint">{t("causes.none")}</Text>
      ) : (
        <>
          <BarRows
            label={t("causes.title")}
            rows={monitor.failure_causes.map((cause) => ({
              key: cause.error_kind,
              label: named(cause.error_kind) ? t(`causes.kind.${cause.error_kind}`) : cause.error_kind,
              value: cause.calls,
              end: t("causes.count", { count: cause.calls, n: format.number(cause.calls) }),
              tone: cause.upstream ? "bad" : "muted",
              link: (
                <Link
                  to="/calls"
                  search={callsDrill({ since: monitor.since, until: monitor.until }, { status: "error" })}
                />
              ),
            }))}
          />
          <Text size="sm" tone="faint">
            {t("causes.legend")}
          </Text>
        </>
      )}
    </Card>
  );
}

// SpreadCard is how long the completed calls took from the request to the last word, as shares of them (the board's
// 完成耗时分布).
export function SpreadCard({ monitor }: { monitor: Monitor }) {
  const { t } = useTranslation("monitor");
  const format = useFormat();
  const rows = ranges(monitor.durations);
  const name = (from: number, to: number | undefined) =>
    to === undefined
      ? t("spread.over", { from: format.number(from) })
      : t("spread.between", { from: format.number(from), to: format.number(to) });
  return (
    <Card title={t("spread.title")} lede={t("spread.lede")}>
      {rows.every((row) => row.share === 0) ? (
        <Text tone="faint">{t("spread.none")}</Text>
      ) : (
        <BarRows
          label={t("spread.title")}
          rows={rows.map((row) => ({
            key: row.from,
            label: name(row.from, row.to),
            value: row.share,
            end: `${row.share.toFixed(1)}%`,
          }))}
        />
      )}
    </Card>
  );
}

// FailedCard is the last calls of the window that did not succeed, each opening its full trace (the board's 最近没有成功的请求).
// It needs the call log's permission; without it the card is left out.
export function FailedCard({ monitor }: { monitor: Monitor }) {
  const { t } = useTranslation("monitor");
  const format = useFormat();
  const allowed = can(useSession(), "calls.search");
  const query = useInfiniteQuery({
    ...adminCallsQuery({ status: "error", since: monitor.since, until: monitor.until }),
    enabled: allowed,
  });
  if (!allowed) {
    return null;
  }
  const calls = (query.data?.pages[0]?.data ?? []).slice(0, 4);
  return (
    <Card
      layout="flush"
      title={t("failed.title")}
      actions={
        <ActionButton asChild size="sm">
          <Link to="/calls" search={callsDrill({ since: monitor.since, until: monitor.until }, { status: "error" })}>
            {t("failed.all")}
          </Link>
        </ActionButton>
      }
    >
      <RowList
        label={t("failed.title")}
        empty={query.isSuccess ? t("failed.none") : undefined}
        items={calls.flatMap((call) =>
          call.id
            ? [
                {
                  key: call.id,
                  tone: "bad" as const,
                  title: call.model ?? call.id,
                  sub: call.error_kind,
                  end: call.at ? format.hour(new Date(call.at)) : undefined,
                  link: <Link to="/calls/$callId" params={{ callId: call.id }} />,
                },
              ]
            : [],
        )}
      />
    </Card>
  );
}
