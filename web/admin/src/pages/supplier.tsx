import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { Button, DataState, Icon, PageHeader, Panel, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Actions, Facts, PageBody, Time } from "../parts";
import { supplierQuery } from "../queries";
import { Checklist } from "../suppliers/checklist";
import {
  BindKeyDialog,
  RemoveDialog,
  ReplaceLoginDialog,
  SelectGroupDialog,
  StatusDialog,
  UnitValueDialog,
} from "../suppliers/dialogs";
import { EndpointTestDialog } from "../suppliers/endpoint-test-dialog";
import { Credentials, Endpoints, Groups, UnboundKeys } from "../suppliers/lists";
import type { Open } from "../suppliers/open";
import { NativeBalance, SupplierState, UnitValue } from "../suppliers/parts";

type Detail = AdminSchemas["SupplierDetail"];

export default function SupplierPage() {
  const { t } = useTranslation();
  const { supplierId } = useParams({ from: "/app/suppliers/$supplierId" });
  const detail = useQuery(supplierQuery(supplierId));
  return (
    <DataState query={detail} page>
      {(data) => (
        <>
          <PageHeader title={data.supplier.name} description={t("suppliers.detail.description")} />
          <PageBody>
            <Body detail={data} />
          </PageBody>
        </>
      )}
    </DataState>
  );
}

function Body({ detail }: { detail: Detail }) {
  const { t } = useTranslation();
  const format = useFormat();
  const manage = can(useSession(), "suppliers.manage");
  const [open, setOpen] = useState<Open | null>(null);
  const { supplier } = detail;
  const done = supplier.status === "removed";
  const act = manage && !done ? setOpen : undefined;
  return (
    <>
      <Actions>
        <Button asChild>
          <Link to="/suppliers">
            <Icon name="left" />
            {t("suppliers.detail.back")}
          </Link>
        </Button>
        {act ? <SupplierActions supplier={supplier} onOpen={act} /> : null}
      </Actions>
      <Checklist detail={detail} onOpen={act} />
      <Facts
        title={t("suppliers.detail.facts")}
        rows={[
          [t("suppliers.col.status"), <SupplierState key="state" supplier={supplier} />],
          [t("suppliers.detail.origin"), supplier.origin],
          [t("suppliers.detail.account"), supplier.upstream_account],
          [
            t("suppliers.detail.kind"),
            [supplier.kind, supplier.generation, supplier.detected_version].filter(Boolean).join(" · "),
          ],
          [t("suppliers.col.balance"), <NativeBalance key="balance" supplier={supplier} />],
          [t("suppliers.detail.observed"), <Time key="observed" at={supplier.balance_observed_at} />],
          [
            t("suppliers.detail.lowLine"),
            supplier.low_balance_threshold ? format.decimal(supplier.low_balance_threshold) : "—",
          ],
          [t("suppliers.col.unit"), <UnitValue key="unit" supplier={supplier} />],
          [t("suppliers.detail.currency"), supplier.settlement_currency],
        ]}
      />
      <Panel title={t("suppliers.groups.title")}>
        <Groups groups={detail.groups} onOpen={act} />
      </Panel>
      <Panel title={t("suppliers.credentials.title")}>
        <Credentials credentials={detail.credentials} groups={detail.groups} />
      </Panel>
      <Panel title={t("suppliers.endpoints.title")}>
        <Endpoints endpoints={detail.endpoints} groups={detail.groups} onOpen={act} />
      </Panel>
      <Panel title={t("suppliers.unbound.title")}>
        <UnboundKeys keys={detail.unbound_keys} onOpen={act} />
      </Panel>
      <Dialogs detail={detail} open={open} onClose={() => setOpen(null)} />
    </>
  );
}

function SupplierActions({ supplier, onOpen }: { supplier: AdminSchemas["Supplier"]; onOpen: (open: Open) => void }) {
  const { t } = useTranslation();
  const { status } = supplier;
  const pausable = status === "active" || status === "low_balance";
  const removable = status !== "removing" && status !== "removed";
  return (
    <>
      {pausable ? (
        <Button onClick={() => onOpen({ kind: "pause" })}>{t("suppliers.status.paused.action")}</Button>
      ) : null}
      {status === "paused" ? (
        <Button onClick={() => onOpen({ kind: "resume" })}>{t("suppliers.status.active.action")}</Button>
      ) : null}
      <Button onClick={() => onOpen({ kind: "unit" })}>{t("suppliers.unit.open")}</Button>
      <Button onClick={() => onOpen({ kind: "login" })}>{t("suppliers.login.open")}</Button>
      {removable ? (
        <Button variant="danger" onClick={() => onOpen({ kind: "remove" })}>
          <Icon name="trash" />
          {t("suppliers.remove.open")}
        </Button>
      ) : null}
    </>
  );
}

function Dialogs({ detail, open, onClose }: { detail: Detail; open: Open | null; onClose: () => void }) {
  const { supplier } = detail;
  switch (open?.kind) {
    case "pause":
      return <StatusDialog supplier={supplier} to="paused" onClose={onClose} />;
    case "resume":
      return <StatusDialog supplier={supplier} to="active" onClose={onClose} />;
    case "remove":
      return <RemoveDialog supplier={supplier} onClose={onClose} />;
    case "login":
      return <ReplaceLoginDialog supplier={supplier} onClose={onClose} />;
    case "unit":
      return <UnitValueDialog supplier={supplier} onClose={onClose} />;
    case "group":
      return <SelectGroupDialog supplier={supplier} group={open.group} onClose={onClose} />;
    case "bind":
      return (
        <BindKeyDialog
          supplier={supplier}
          upstreamKey={open.key}
          groups={detail.groups.filter((group) => group.selected)}
          onClose={onClose}
        />
      );
    case "test":
      return <EndpointTestDialog endpoint={open.endpoint} onClose={onClose} />;
    default:
      return null;
  }
}
