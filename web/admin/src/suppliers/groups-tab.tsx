import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { Card, DataTable, EmptyState, LinkButton, MetricStrip, Note, Switch, Tag, Text, useFormat } from "@axiom/ui";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useLines } from "../lines";
import { Code } from "../parts";
import type { SupplierView } from "../queries";
import { NativeBalance, Rate, useTraffic } from "./parts";
import { Setup } from "./setup";
import { AddKeysDialog, ConnectGroupDialog, PauseGroupDialog } from "./supplier-dialogs";
import { credentialName, modeOf } from "./upstream";

type Group = AdminSchemas["SupplierGroup"];
type Credential = AdminSchemas["SupplierCredential"];
type Open = { kind: "connect" | "pause"; group: Group } | { kind: "paste" };

// A ratio change or a new group is news for a week (the queue's ratio_changed and group_added look as far back).
const week = 7 * 24 * 60 * 60 * 1000;
const recent = (at: string | undefined) => at !== undefined && Date.now() - Date.parse(at) < week;

// 分组, or 线路 for a direct API and a cloud platform (adPg_supplier's groups tab): the checklist while the channel is
// not ready, how its traffic and balance stand, and every group the relay lists, taken in or not.
export default function GroupsTab({ detail }: { detail: SupplierView }) {
  const { t } = useTranslation();
  const relay = modeOf(detail.supplier) === "relay";
  const { supplier } = detail;
  return (
    <>
      {supplier.status_reason ? (
        <Note title={t(`suppliers.state.${supplier.status}`)}>{supplier.status_reason}</Note>
      ) : null}
      <Setup detail={detail} />
      <Strip detail={detail} />
      {relay ? <GroupTable detail={detail} /> : <LineTable detail={detail} />}
    </>
  );
}

function Strip({ detail }: { detail: SupplierView }) {
  const { t } = useTranslation();
  const traffic = useTraffic();
  const { supplier } = detail;
  const own = traffic?.ofSupplier(supplier.id);
  const days = supplier.balance_days;
  const items = [
    ...(own
      ? [
          {
            key: "rate",
            label: t("upstream.detail.rate"),
            value: own.rate === undefined ? "—" : (own.rate * 100).toFixed(2),
            suffix: own.rate === undefined ? undefined : "%",
            note: own.calls === 0 ? t("upstream.detail.noTraffic") : undefined,
          },
          {
            key: "share",
            label: t("upstream.detail.share"),
            value: (own.share * 100).toFixed(0),
            suffix: "%",
            note: t("upstream.detail.shareNote", { count: detail.groups.filter((group) => group.selected).length }),
          },
        ]
      : []),
    {
      key: "balance",
      label: t("upstream.detail.balance"),
      value: modeOf(supplier) === "relay" ? <NativeBalance supplier={supplier} /> : t("upstream.detail.noBalance"),
      note:
        days === undefined ? undefined : days < 5 ? (
          <Text as="span" tone="warn">
            {t("upstream.detail.balanceShort", { count: days })}
          </Text>
        ) : (
          t("upstream.detail.balanceDays", { count: days })
        ),
    },
  ];
  return <MetricStrip label={t("upstream.detail.strip")} items={items} />;
}

// The credential a group's calls go with, if it has one that is not erased.
const credentialOf = (credentials: readonly Credential[], group: Group) =>
  credentials.find((credential) => credential.group_id === group.id && credential.status !== "erased");

function GroupTable({ detail }: { detail: SupplierView }) {
  const { t } = useTranslation();
  const format = useFormat();
  const traffic = useTraffic();
  const manage = can(useSession(), "suppliers.manage") && detail.supplier.status !== "removed";
  const [open, setOpen] = useState<Open | null>(null);
  const { groups, credentials } = detail;
  const toggle = (group: Group) => {
    const token = credentialOf(credentials, group);
    // A group with no token yet, which the sync would have to make one for, is connected with one in the same change;
    // a node of an earlier release does not say how a group's key stands, and takes the plain choice.
    const fresh = !group.selected && !token && group.key_state !== undefined && group.key_state !== "unavailable";
    setOpen({ kind: fresh ? "connect" : "pause", group });
  };
  const columns = groupColumns({
    t,
    format,
    traffic,
    credentials,
    manage,
    toggle,
    paste: () => setOpen({ kind: "paste" }),
  });
  return (
    <Card
      layout="flush"
      foot={t("upstream.groups.foot")}
      title={t("upstream.groups.title")}
      lede={t("upstream.groups.lede")}
      actions={
        groups.length === 0 ? undefined : (
          <Text as="span" size="sm" tone="faint" numeric>
            {t("upstream.groups.inUse", { count: groups.filter((g) => g.selected).length, all: groups.length })}
          </Text>
        )
      }
    >
      {groups.length === 0 ? (
        <EmptyState compact title={t("suppliers.groups.empty.title")}>
          {t("suppliers.groups.empty.body")}
        </EmptyState>
      ) : (
        <DataTable label={t("upstream.groups.title")} rows={groups} rowKey={(group) => group.id} columns={columns} />
      )}
      <GroupDialogs detail={detail} open={open} onClose={() => setOpen(null)} />
    </Card>
  );
}

function GroupDialogs({ detail, open, onClose }: { detail: SupplierView; open: Open | null; onClose: () => void }) {
  const { supplier, groups } = detail;
  switch (open?.kind) {
    case "connect":
      return <ConnectGroupDialog supplier={supplier} group={open.group} onClose={onClose} />;
    case "pause":
      return <PauseGroupDialog supplier={supplier} group={open.group} onClose={onClose} />;
    case "paste":
      return <AddKeysDialog supplier={supplier} groups={groups.filter((g) => g.selected)} onClose={onClose} />;
    default:
      return null;
  }
}

type Columns = {
  t: TFunction<"admin">;
  format: ReturnType<typeof useFormat>;
  traffic: ReturnType<typeof useTraffic>;
  credentials: readonly Credential[];
  manage: boolean;
  toggle: (group: Group) => void;
  paste: () => void;
};

// The groups table's columns (adPg_supplier's adgt): the switch, the name, the ratio and its change, the models, the
// token, how it does and its traffic, which only a session that may read the monitor has.
function groupColumns({ t, format, traffic, credentials, manage, toggle, paste }: Columns) {
  const share = traffic
    ? [
        {
          key: "share",
          header: t("upstream.groups.col.share"),
          align: "end" as const,
          from: "md" as const,
          cell: (group: Group) => (group.selected ? `${(traffic.ofGroup(group.id).share * 100).toFixed(0)}%` : "—"),
        },
      ]
    : [];
  return [
    {
      key: "on",
      header: t("upstream.groups.col.on"),
      cell: (group: Group) => (
        <Switch
          label={t(group.selected ? "upstream.groups.switchOff" : "upstream.groups.switchOn", { name: group.name })}
          checked={group.selected}
          disabled={!manage || (!group.selected && !group.present)}
          onChange={() => toggle(group)}
        />
      ),
    },
    { key: "name", header: t("upstream.groups.col.name"), cell: (group: Group) => <GroupName group={group} /> },
    {
      key: "ratio",
      header: t("upstream.groups.col.ratio"),
      align: "end" as const,
      cell: (group: Group) =>
        group.previous_ratio !== undefined && recent(group.ratio_changed_at) ? (
          <Text as="span" tone="warn" numeric>
            {`${format.decimal(group.previous_ratio)} → ${format.decimal(group.ratio)}`}
          </Text>
        ) : (
          format.decimal(group.ratio)
        ),
    },
    {
      key: "models",
      header: t("upstream.groups.col.models"),
      align: "end" as const,
      from: "md" as const,
      cell: (group: Group) => (group.model_count === undefined ? "—" : format.number(group.model_count)),
    },
    {
      key: "token",
      header: t("upstream.groups.col.token"),
      from: "lg" as const,
      cell: (group: Group) => (
        <Token group={group} token={credentialOf(credentials, group)} manage={manage} onPaste={paste} />
      ),
    },
    {
      key: "rate",
      header: t("upstream.groups.col.rate"),
      cell: (group: Group) => <GroupState group={group} rate={traffic?.ofGroup(group.id).rate} />,
    },
    ...share,
  ];
}

function GroupName({ group }: { group: Group }) {
  const { t } = useTranslation();
  const fresh = !group.selected && !group.codename && recent(group.first_seen_at);
  return (
    <>
      <Code>{group.name}</Code>
      {fresh ? (
        <>
          {" "}
          <Tag tone="accent">{t("upstream.groups.fresh")}</Tag>
        </>
      ) : null}
      {group.description ? (
        <Text size="sm" tone="muted">
          {group.description}
        </Text>
      ) : null}
      {group.key_state === "failed" && group.key_reason ? (
        <Text size="sm" tone="bad">
          {t(`suppliers.groups.keyReason.${group.key_reason}`)}
        </Text>
      ) : null}
    </>
  );
}

function Token({
  group,
  token,
  manage,
  onPaste,
}: {
  group: Group;
  token: Credential | undefined;
  manage: boolean;
  onPaste: () => void;
}) {
  const { t } = useTranslation();
  if (token) return <Code>{credentialName(token)}</Code>;
  if (!group.selected) {
    return (
      <Text as="span" tone="muted">
        {t("upstream.groups.makeAtConnect")}
      </Text>
    );
  }
  return manage ? <LinkButton onClick={onPaste}>{t("upstream.groups.paste")}</LinkButton> : "—";
}

function GroupState({ group, rate }: { group: Group; rate: number | undefined }) {
  const { t } = useTranslation();
  const said = !group.present
    ? "gone"
    : !group.selected
      ? group.codename
        ? "paused"
        : "notTaken"
      : rate === undefined
        ? "waiting"
        : undefined;
  return said ? (
    <Text as="span" tone="muted">
      {t(`upstream.groups.${said}`)}
    </Text>
  ) : (
    <Rate rate={rate} />
  );
}

// 线路 for a direct API or a cloud platform: its one line, how it does, and the keys or regions it calls with.
function LineTable({ detail }: { detail: SupplierView }) {
  const { t } = useTranslation();
  const traffic = useTraffic();
  const { byGroup } = useLines();
  const lines = detail.groups.filter((group) => group.selected);
  return (
    <Card layout="flush" title={t("upstream.lines.title")} lede={t("upstream.lines.lede")}>
      {lines.length === 0 ? (
        <EmptyState compact title={t("upstream.lines.none")} />
      ) : (
        <DataTable
          label={t("upstream.lines.title")}
          rows={lines}
          rowKey={(group) => group.id}
          columns={[
            {
              key: "line",
              header: t("upstream.lines.col.line"),
              cell: (group) => byGroup.get(group.id)?.name ?? detail.supplier.name,
            },
            {
              key: "rate",
              header: t("upstream.lines.col.rate"),
              cell: (group) => <Rate rate={traffic?.ofGroup(group.id).rate} />,
            },
            {
              key: "first",
              header: t("upstream.lines.col.firstToken"),
              align: "end",
              from: "md",
              cell: (group) => {
                const ms = traffic?.ofGroup(group.id).firstTokenMs;
                return ms === undefined ? "—" : t("upstream.lines.seconds", { value: (ms / 1000).toFixed(2) });
              },
            },
            {
              key: "accounts",
              header: t("upstream.lines.col.accounts"),
              from: "md",
              cell: (group) =>
                t("upstream.lines.keysCount", {
                  count: detail.credentials.filter((c) => c.group_id === group.id && c.status !== "erased").length,
                }),
            },
          ]}
        />
      )}
    </Card>
  );
}
