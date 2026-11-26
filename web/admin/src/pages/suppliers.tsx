import { useQuery } from "@tanstack/react-query";
import { useSearch } from "@tanstack/react-router";
import { lazy, Suspense } from "react";
import { useTranslation } from "react-i18next";
import { NewButton } from "../parts";
import { inboxSummaryQuery } from "../queries";
import { Lazily } from "../suppliers/tabs";
import { TabbedPage, useTabs } from "../tabbed-page";

// Each view is its own chunk, loaded when its tab opens: the page stays within a page's budget with four of them.
const Channels = lazy(() => import("../suppliers/channels-tab"));
const ByModel = lazy(() => import("../suppliers/by-model-tab"));
const Inbox = lazy(() => import("../suppliers/inbox-tab"));
const Keys = lazy(() => import("../suppliers/keys-tab"));
const AddUpstream = lazy(() => import("../suppliers/add-dialog"));

// 上游 (adPg_suppliers in admin_pages_f.js): what is connected, seen four ways. The channels, a row per model with the
// lines that serve it, what wants a decision from every channel, and every key across them. Each opens a channel, whose
// page holds its groups, models, keys and sync (pages/supplier.tsx).
export default function Upstream() {
  const { t } = useTranslation();
  const search = useSearch({ from: "/app/suppliers" });
  const tab: "channels" | "models" | "inbox" | "keys" = search.tab ?? "channels";
  const inbox = useQuery(inboxSummaryQuery);
  const tabs = useTabs(
    "/suppliers",
    [
      { key: "channels", label: t("upstream.tabs.channels") },
      { key: "models", label: t("upstream.tabs.models") },
      { key: "inbox", label: t("upstream.tabs.inbox"), count: inbox.data?.total || undefined },
      { key: "keys", label: t("upstream.tabs.keys") },
    ],
    tab,
  );
  return (
    <TabbedPage
      title={t("upstream.title")}
      lede={t(`upstream.lede.${tab}`)}
      label={t("upstream.tabs.label")}
      tabs={tabs}
      actions={
        <NewButton permission="suppliers.manage" label={t("upstream.add")}>
          {(close) => (
            <Suspense fallback={null}>
              <AddUpstream onClose={close} />
            </Suspense>
          )}
        </NewButton>
      }
    >
      <Lazily>
        {tab === "channels" ? <Channels /> : null}
        {tab === "models" ? <ByModel /> : null}
        {tab === "inbox" ? <Inbox /> : null}
        {tab === "keys" ? <Keys credential={search.credential} /> : null}
      </Lazily>
    </TabbedPage>
  );
}

export { default as messages } from "../locales/suppliers";
