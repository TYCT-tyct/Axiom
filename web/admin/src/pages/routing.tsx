import { useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { DefaultsTab } from "../routing/defaults-tab";
import { PresetsTab } from "../routing/presets-tab";
import { TabbedPage, useTabs } from "../tabbed-page";
import SourceClasses from "./source-classes";

// Routing in the admin's one page (adPg_routing in admin_pages_b.js): the presets that say where a request goes first
// and where next, what the layers hold together, and the source classes lines are told apart by.
export default function Routing() {
  const { t } = useTranslation();
  const search = useSearch({ from: "/app/routing" });
  const tab: "presets" | "defaults" | "sources" = search.tab ?? "presets";
  const tabs = useTabs(
    "/routing",
    [
      { key: "presets", label: t("routingPresets.tabs.presets") },
      { key: "defaults", label: t("routingPresets.tabs.defaults") },
      { key: "sources", label: t("routingPresets.tabs.sources") },
    ],
    tab,
  );
  return (
    <TabbedPage
      title={t("routingPresets.title")}
      lede={t(`routingPresets.lede.${tab}`)}
      label={t("routingPresets.tabs.label")}
      tabs={tabs}
    >
      {tab === "presets" ? <PresetsTab selected={search.preset} /> : null}
      {tab === "defaults" ? <DefaultsTab /> : null}
      {tab === "sources" ? <SourceClasses /> : null}
    </TabbedPage>
  );
}

export { default as messages } from "../locales/routing";
