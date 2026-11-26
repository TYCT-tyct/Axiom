import { PageTabs } from "@axiom/ui";
import { useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { usePending } from "./pending";

// The two tabs of 运维监控 (adTabDef.ops): the overview and the alerts. Each is a page of its own, so an old link to
// /alert-events, the bell and the palette still land on the alerts, and the tab bar is the same on both.
export function MonitorTabs({ current }: { current: "overview" | "alerts" }) {
  const { t } = useTranslation("monitor");
  const navigate = useNavigate();
  const { alerts } = usePending();
  return (
    <PageTabs
      label={t("tabs")}
      items={[
        {
          label: t("overview"),
          current: current === "overview",
          href: "/monitor",
          onSelect: () => navigate({ to: "/monitor", search: { window: "1h" } }),
        },
        {
          label: t("alerts"),
          count: alerts,
          current: current === "alerts",
          href: "/alert-events",
          onSelect: () => navigate({ to: "/alert-events" }),
        },
      ]}
    />
  );
}
