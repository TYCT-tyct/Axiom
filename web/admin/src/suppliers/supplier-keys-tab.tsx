import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { ActionButton, Card, Cluster, EmptyState } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { notBuilt } from "../monitor-queries";
import type { SupplierView } from "../queries";
import { TestAllDialog } from "./credential-dialogs";
import { CredentialDrawer } from "./credential-drawer";
import { CredentialTable, useSuppliers } from "./credentials";
import { BindKeyDialog } from "./dialogs";
import { UnboundKeys } from "./lists";
import { stepsOf } from "./parts";
import { credentialsListQuery } from "./queries";
import { AddKeysDialog, ReplaceCloudDialog } from "./supplier-dialogs";
import { modeOf } from "./upstream";

type Credential = AdminSchemas["SupplierCredential"];
type Open = "add" | "replace" | "test" | { bind: AdminSchemas["UpstreamKey"] };

// Key (adPg_supplier's keys tab): the channel's own credentials, with what each holds now where the node counts it,
// and for a relay the keys it lists that Axiom has not bound. A relay takes a pasted token for a group, a direct API
// more keys, a cloud platform a new credential.
export default function SupplierKeysTab({ detail, credential }: { detail: SupplierView; credential?: string }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { supplier } = detail;
  const list = useInfiniteQuery(credentialsListQuery({ supplier_id: supplier.id }));
  const { suppliers, lineOf } = useSuppliers();
  // A node of an earlier release lists no credentials across suppliers: the supplier's own read has them, without the
  // live counts.
  const rows: Credential[] = notBuilt(list.error)
    ? detail.credentials
    : (list.data?.pages.flatMap((page) => page.data) ?? detail.credentials);
  const known = new Map(suppliers).set(supplier.id, supplier);
  const show = (id: string | undefined, replace = false) =>
    void navigate({
      to: "/suppliers/$supplierId",
      params: { supplierId: supplier.id },
      search: { tab: "keys", credential: id },
      replace,
    });
  const index = rows.findIndex((row) => row.id === credential);
  const opened = rows[index];
  return (
    <>
      <Card layout="flush" actions={<KeyActions detail={detail} />}>
        {rows.length === 0 ? (
          <EmptyState compact title={t("upstream.keys.empty.title")}>
            {t("upstream.keys.empty.body")}
          </EmptyState>
        ) : (
          <CredentialTable
            rows={rows}
            total={rows.length}
            suppliers={known}
            lineOf={lineOf}
            across={false}
            onOpen={(row) => show(row.id)}
            current={credential}
          />
        )}
      </Card>
      {modeOf(supplier) === "relay" ? <Unbound detail={detail} /> : null}
      {opened ? (
        <CredentialDrawer
          credential={opened}
          supplier={supplier}
          line={lineOf(opened)}
          all={rows}
          steps={stepsOf(t, index, rows, (id) => show(id, true))}
          onClose={() => show(undefined)}
        />
      ) : null}
    </>
  );
}

function KeyActions({ detail }: { detail: SupplierView }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState<Open | null>(null);
  const { supplier } = detail;
  const mode = modeOf(supplier);
  if (!can(useSession(), "suppliers.manage") || supplier.status === "removed") return null;
  const close = () => setOpen(null);
  return (
    <Cluster>
      <ActionButton size="sm" icon="status" onClick={() => setOpen("test")}>
        {t("upstream.keys.testAll")}
      </ActionButton>
      <ActionButton size="sm" icon="plus" onClick={() => setOpen(mode === "cloud" ? "replace" : "add")}>
        {t(
          mode === "relay"
            ? "upstream.groups.paste"
            : mode === "cloud"
              ? "upstream.credential.replace"
              : "upstream.addKeys.open",
        )}
      </ActionButton>
      {open === "test" ? <TestAllDialog supplierId={supplier.id} onClose={close} /> : null}
      {open === "add" ? (
        <AddKeysDialog supplier={supplier} groups={detail.groups.filter((group) => group.selected)} onClose={close} />
      ) : null}
      {open === "replace" ? <ReplaceCloudDialog supplier={supplier} onClose={close} /> : null}
    </Cluster>
  );
}

// The keys a relay lists that no group uses: one is bound to a group, for a key the sync did not make.
function Unbound({ detail }: { detail: SupplierView }) {
  const { t } = useTranslation();
  const manage = can(useSession(), "suppliers.manage") && detail.supplier.status !== "removed";
  const [bind, setBind] = useState<AdminSchemas["UpstreamKey"] | null>(null);
  return (
    <Card layout="flush" title={t("upstream.unbound")}>
      <UnboundKeys
        keys={detail.unbound_keys}
        onOpen={manage ? (open) => open.kind === "bind" && setBind(open.key) : undefined}
      />
      {bind ? (
        <BindKeyDialog
          supplier={detail.supplier}
          upstreamKey={bind}
          groups={detail.groups.filter((group) => group.selected)}
          onClose={() => setBind(null)}
        />
      ) : null}
    </Card>
  );
}
