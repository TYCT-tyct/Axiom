import { type AdminSchemas, ApiError } from "@axiom/api";
import { can, RulesSummary, systemPreset, useSession } from "@axiom/app";
import {
  ActionButton,
  Card,
  Cluster,
  DataState,
  DataTable,
  EmptyState,
  LinkButton,
  SegmentedControl,
  Tag,
  Text,
} from "@axiom/ui";
import { type UseQueryResult, useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { type Lines, useLines } from "../lines";
import { Code } from "../parts";
import { PresetDialog } from "../preset-dialogs";
import { accountQuery, layerDefaultQuery, layerPresetsQuery, type PresetLayer } from "../queries";
import { SuspendedNotice, usePresetsHold } from "../routing-suspension";
import { orderOf } from "./line-order";
import { PresetDrawer } from "./preset-drawer";
import { PresetKeys } from "./preset-keys";

type Preset = AdminSchemas["RoutingPreset"];
type Row = { preset: Preset; layer: PresetLayer };
type Scope = "all" | "platform" | "site";

// PresetsTab lists the two layers above the accounts in one table, the platform's presets and those of the site the
// admin's account is of (the admin has no list of sites): a preset opens in a drawer, which the address names
// (`?preset=`). A node of a release before these operations answers 404, which is said, not shown as a missing page.
export function PresetsTab({ selected }: { selected: string | undefined }) {
  const { t } = useTranslation();
  const owner = useQuery(accountQuery(useSession().account));
  const platform = useQuery(layerPresetsQuery({ scope: "platform" }));
  if (platform.error instanceof ApiError && platform.error.status === 404) {
    return (
      <Card title={t("routingPresets.unavailableTitle")}>
        <Text tone="muted">{t("routingPresets.unavailableBody")}</Text>
      </Card>
    );
  }
  return (
    <>
      <SuspendedNotice />
      <DataState query={owner}>
        {(account) => (
          <Presets
            platform={platform}
            siteId={account.site_id}
            currency={account.balance.balance.currency}
            selected={selected}
          />
        )}
      </DataState>
    </>
  );
}

function Presets({
  platform,
  siteId,
  currency,
  selected,
}: {
  platform: UseQueryResult<AdminSchemas["RoutingPresetList"]>;
  siteId: AdminSchemas["SiteId"];
  currency: string;
  selected: string | undefined;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const manage = can(useSession(), "sites.manage");
  const lines = useLines();
  const siteLayer: PresetLayer = { scope: "site", siteId };
  const site = useQuery(layerPresetsQuery(siteLayer));
  const platformDefault = useQuery(layerDefaultQuery({ scope: "platform" })).data?.preset_id;
  const siteDefault = useQuery(layerDefaultQuery(siteLayer)).data?.preset_id;
  const [scope, setScope] = useState<Scope>("all");
  const [creating, setCreating] = useState<PresetLayer>();
  const rows: Row[] = [
    ...(platform.data?.data ?? []).map((preset) => ({ preset, layer: { scope: "platform" } as PresetLayer })),
    ...(site.data?.data ?? []).map((preset) => ({ preset, layer: siteLayer })),
  ];
  const shown = rows.filter((row) => scope === "all" || row.layer.scope === scope);
  // The table is of both layers: it waits for both, and fails with either.
  const both = {
    status: platform.isError || site.isError ? "error" : platform.isSuccess && site.isSuccess ? "success" : "pending",
    data: rows,
    error: platform.error ?? site.error,
    errorUpdatedAt: Math.max(platform.errorUpdatedAt, site.errorUpdatedAt),
    refetch: () => Promise.all([platform.refetch(), site.refetch()]),
  } as const;
  const open = (id: string | undefined, replace = false) =>
    void navigate({ to: "/routing", search: { tab: undefined, preset: id }, replace });
  const index = shown.findIndex((row) => row.preset.id === selected);
  const chosen = shown[index];
  const defaultOf = (row: Row) => (row.layer.scope === "platform" ? platformDefault : siteDefault) === row.preset.id;
  return (
    <>
      <Toolbar
        scope={scope}
        onScope={setScope}
        onCreate={manage ? (layer) => setCreating(layer === "platform" ? { scope: "platform" } : siteLayer) : undefined}
      />
      <Card layout="flush">
        <DataState
          query={both}
          isEmpty={() => shown.length === 0}
          empty={<EmptyState title={t("routingPresets.empty")} />}
        >
          {() => <PresetsTable rows={shown} lines={lines} defaultOf={defaultOf} onOpen={(id) => open(id)} />}
        </DataState>
      </Card>
      {creating ? <PresetDialog layer={creating} currency={currency} onClose={() => setCreating(undefined)} /> : null}
      {chosen ? (
        <PresetDrawer
          preset={chosen.preset}
          layer={chosen.layer}
          currency={currency}
          siteId={siteId}
          lines={lines}
          manage={manage}
          index={index}
          count={shown.length}
          onStep={(by) => open(shown[index + by]?.preset.id, true)}
          onClose={() => open(undefined)}
        />
      ) : null}
    </>
  );
}

// What the preset says of where requests go: its line order, when it has one, else what its rules keep.
function Order({ row, lines }: { row: Row; lines: Lines }) {
  const order = orderOf(row.preset.rules);
  if (order.length === 0) {
    return <RulesSummary summary={row.preset.summary} />;
  }
  return (
    <>
      <Text as="span">{order.map((code) => lines.byCodename.get(code)?.name ?? code).join(" → ")}</Text>
      <RulesSummary summary={row.preset.summary} />
    </>
  );
}

function Toolbar({
  scope,
  onScope,
  onCreate,
}: {
  scope: Scope;
  onScope: (scope: Scope) => void;
  onCreate: ((layer: "platform" | "site") => void) | undefined;
}) {
  const { t } = useTranslation();
  const hold = usePresetsHold();
  return (
    <Cluster justify="between">
      <SegmentedControl
        label={t("routingPresets.scope.label")}
        value={scope}
        options={[
          { value: "all", label: t("routingPresets.scope.all") },
          { value: "platform", label: t("routingPresets.scope.platform") },
          { value: "site", label: t("routingPresets.scope.site") },
        ]}
        onChange={onScope}
      />
      {onCreate ? (
        <Cluster>
          <ActionButton icon="plus" onClick={() => onCreate("platform")} {...hold}>
            {t("routingPresets.createTitle.platform")}
          </ActionButton>
          <ActionButton variant="pri" icon="plus" onClick={() => onCreate("site")} {...hold}>
            {t("routingPresets.createTitle.site")}
          </ActionButton>
        </Cluster>
      ) : null}
    </Cluster>
  );
}

function PresetsTable({
  rows,
  lines,
  defaultOf,
  onOpen,
}: {
  rows: readonly Row[];
  lines: Lines;
  defaultOf: (row: Row) => boolean;
  onOpen: (id: string) => void;
}) {
  const { t } = useTranslation();
  return (
    <DataTable
      fixed
      label={t("routingPresets.title")}
      rows={rows}
      rowKey={(row) => row.preset.id}
      columns={[
        {
          key: "name",
          width: 320,
          header: t("routingPresets.col.name"),
          cell: (row) => (
            <Cluster gap={8}>
              <LinkButton
                label={t("routingPresets.open", { name: row.preset.name })}
                onClick={() => onOpen(row.preset.id)}
              >
                {row.preset.name}
              </LinkButton>
              <Code>{row.preset.slug}</Code>
              {row.preset.id === systemPreset ? <Tag>{t("routingPresets.system")}</Tag> : null}
              {defaultOf(row) ? <Tag tone="accent">{t(`routingPresets.defaultTag.${row.layer.scope}`)}</Tag> : null}
              {row.preset.disabled ? <Tag tone="warn">{t("routingPresets.disabled")}</Tag> : null}
              {row.preset.currency ? <Tag>{row.preset.currency}</Tag> : null}
            </Cluster>
          ),
        },
        {
          key: "scope",
          width: 100,
          header: t("routingPresets.col.scope"),
          cell: (row) => t(`routingPresets.scope.${row.layer.scope}`),
        },
        {
          key: "rules",
          width: 300,
          header: t("routingPresets.col.order"),
          cell: (row) => <Order row={row} lines={lines} />,
        },
        {
          key: "version",
          width: 90,
          header: t("routingPresets.col.version"),
          align: "end",
          cell: (row) => t("routingPresets.version", { version: row.preset.version }),
        },
        {
          key: "keys",
          width: 120,
          header: t("routingPresets.col.keys"),
          align: "end",
          sort: (row) => row.preset.keys,
          cell: (row) => <PresetKeys preset={row.preset} />,
        },
      ]}
    />
  );
}
