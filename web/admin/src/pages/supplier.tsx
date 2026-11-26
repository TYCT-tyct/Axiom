import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { ActionButton, Cluster, DataState, Icon, Tag } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams, useSearch } from "@tanstack/react-router";
import { lazy, useState } from "react";
import { useTranslation } from "react-i18next";
import { type SupplierView, supplierQuery } from "../queries";
import { StatusDialog } from "../suppliers/dialogs";
import { Lazily } from "../suppliers/tabs";
import { modeOf } from "../suppliers/upstream";
import { TabbedPage, useTabs } from "../tabbed-page";

// Each tab is its own chunk, loaded when it opens.
const Groups = lazy(() => import("../suppliers/groups-tab"));
const Models = lazy(() => import("../suppliers/supplier-models-tab"));
const Keys = lazy(() => import("../suppliers/supplier-keys-tab"));
const Sync = lazy(() => import("../suppliers/sync-tab"));
const Settings = lazy(() => import("../suppliers/settings-tab"));

type Tab = "groups" | "models" | "keys" | "sync" | "settings";

// One channel (adPg_supplier in admin_pages_f.js): its groups (a direct API's or a cloud platform's one line), its
// models in four piles, its keys, what the sync found, and its settings. Pausing it sits beside the title.
export default function SupplierPage() {
  const { supplierId } = useParams({ from: "/app/suppliers/$supplierId" });
  const detail = useQuery(supplierQuery(supplierId));
  return (
    <DataState query={detail} page>
      {(data) => <Channel detail={data} />}
    </DataState>
  );
}

const tones = {
  connecting: "warn",
  active: "ok",
  low_balance: "warn",
  needs_attention: "bad",
  paused: "neutral",
  removing: "neutral",
  removed: "neutral",
} as const satisfies Record<AdminSchemas["SupplierStatus"], "ok" | "warn" | "bad" | "neutral">;

function Channel({ detail }: { detail: SupplierView }) {
  const { t } = useTranslation();
  const search = useSearch({ from: "/app/suppliers/$supplierId" });
  const { supplier } = detail;
  const relay = modeOf(supplier) === "relay";
  const tab: Tab = search.tab ?? "groups";
  const tabs = useTabs(
    "/suppliers/$supplierId",
    [
      { key: "groups", label: t(relay ? "upstream.detail.tabs.groups" : "upstream.detail.tabs.lines") },
      { key: "models", label: t("upstream.detail.tabs.models") },
      { key: "keys", label: t("upstream.detail.tabs.keys") },
      { key: "sync", label: t("upstream.detail.tabs.sync") },
      { key: "settings", label: t("upstream.detail.tabs.settings") },
    ],
    tab,
    { supplierId: supplier.id },
  );
  return (
    <TabbedPage
      title={supplier.name}
      tag={
        <Tag tone={tones[supplier.status]} dot>
          {t(`suppliers.state.${supplier.status}`)}
        </Tag>
      }
      lede={`${supplier.kind}　${supplier.origin}`}
      label={t("upstream.detail.tabs.label")}
      tabs={tabs}
      actions={<Actions supplier={supplier} />}
    >
      <Lazily>
        {tab === "groups" ? <Groups detail={detail} /> : null}
        {tab === "models" ? <Models detail={detail} endpoint={search.endpoint} /> : null}
        {tab === "keys" ? <Keys detail={detail} credential={search.credential} /> : null}
        {tab === "sync" ? <Sync detail={detail} /> : null}
        {tab === "settings" ? <Settings detail={detail} /> : null}
      </Lazily>
    </TabbedPage>
  );
}

// Back to the list, and 暂停接流量 or 恢复接流量 for a session that may change suppliers.
function Actions({ supplier }: { supplier: AdminSchemas["Supplier"] }) {
  const { t } = useTranslation();
  const manage = can(useSession(), "suppliers.manage");
  const [open, setOpen] = useState<"paused" | "active" | null>(null);
  const pausable = supplier.status === "active" || supplier.status === "low_balance";
  return (
    <Cluster>
      <ActionButton asChild>
        <Link to="/suppliers">
          <Icon name="left" />
          {t("upstream.detail.back")}
        </Link>
      </ActionButton>
      {manage && pausable ? (
        <ActionButton onClick={() => setOpen("paused")}>{t("upstream.detail.pause")}</ActionButton>
      ) : null}
      {manage && supplier.status === "paused" ? (
        <ActionButton onClick={() => setOpen("active")}>{t("upstream.detail.resume")}</ActionButton>
      ) : null}
      {open ? <StatusDialog supplier={supplier} to={open} onClose={() => setOpen(null)} /> : null}
    </Cluster>
  );
}

export { default as messages } from "../locales/suppliers";
