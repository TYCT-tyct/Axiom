import { MetricStrip, type StripItem, useFormat } from "@axiom/ui";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { failureSplit, type Monitor, perMinute, switchRate } from "./monitor";
import { callsDrill } from "./overview";

// The monitor's six numbers over the window (adk6): requests and tokens a minute, how many failed, how long the first
// token and the whole answer took, and how often a failed call went on to another line. A time is a median with the
// 95th percentile as its sub line; a number the window could not measure (no call had it) says so.
export function Figures({ monitor }: { monitor: Monitor }) {
  const { t } = useTranslation("monitor");
  const format = useFormat();
  const none = t("none");
  const requests = perMinute(monitor, (bucket) => bucket.calls, monitor.calls);
  const tokens = perMinute(monitor, (bucket) => bucket.tokens, monitor.tokens);
  const split = failureSplit(monitor);
  const switched = switchRate(monitor);
  const seconds = (ms: number | undefined, digits: number) => (ms === undefined ? none : (ms / 1000).toFixed(digits));
  const per = (value: number) => format.decimal(value < 10 ? value.toFixed(1) : String(Math.round(value)));
  const items: StripItem[] = [
    {
      key: "requests",
      label: t("requestsPerMinute"),
      value: per(requests.average),
      note: t("peak", { value: per(requests.peak) }),
      link: <Link to="/calls" search={callsDrill({ since: monitor.since, until: monitor.until })} />,
    },
    {
      key: "tokens",
      label: t("tokensPerMinute"),
      value: format.compact(tokens.average),
      note: t("tokensNote"),
    },
    {
      key: "failure",
      label: t("failure"),
      value: monitor.calls > 0 ? ((monitor.failures / monitor.calls) * 100).toFixed(2) : none,
      suffix: monitor.calls > 0 ? "%" : undefined,
      note: t("failureSplit", { upstream: format.number(split.upstream), own: format.number(split.own) }),
      link: (
        <Link to="/calls" search={callsDrill({ since: monitor.since, until: monitor.until }, { status: "error" })} />
      ),
    },
    {
      key: "firstToken",
      label: t("firstToken"),
      value: seconds(monitor.first_token_ms.p50, 2),
      suffix: monitor.first_token_ms.p50 === undefined ? undefined : t("seconds"),
      note: t("within", { value: seconds(monitor.first_token_ms.p95, 2) }),
    },
    {
      key: "total",
      label: t("total"),
      value: seconds(monitor.total_ms.p50, 1),
      suffix: monitor.total_ms.p50 === undefined ? undefined : t("seconds"),
      note: t("within", { value: seconds(monitor.total_ms.p95, 1) }),
    },
    {
      key: "switch",
      label: t("switch"),
      value: switched === undefined ? none : switched.toFixed(1),
      suffix: switched === undefined ? undefined : "%",
      note: t("switchNote"),
      link: <Link to="/routing-presets" />,
    },
  ];
  return <MetricStrip label={t("figures")} columns={3} items={items} />;
}
