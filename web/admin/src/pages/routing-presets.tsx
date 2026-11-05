import { type AdminSchemas, ApiError } from "@axiom/api";
import { can, RulesSummary, useSession } from "@axiom/app";
import { Badge, Button, DataState, EmptyState, PageHeader, Panel, Table, Td, useFormat } from "@axiom/ui";
import { type UseQueryResult, useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Code, Head, PageBody } from "../parts";
import { DefaultCard, DisablePresetDialog, PresetDialog, systemPreset } from "../preset-dialogs";
import { accountQuery, layerPresetsQuery, type PresetLayer } from "../queries";
import { RoutingLimits } from "../routing-limits";

type Preset = AdminSchemas["RoutingPreset"];
// The dialog open on a layer: a new preset, or a change to one, followed by ID in the list so a refresh after a stale
// revision hands it the revision it must now send.
type Open = { kind: "create" } | { kind: "edit" | "disable"; id: string } | null;

// RoutingPresetsPage manages the two layers above the accounts: the platform's presets and default, which every site's
// keys may use, and the presets and default of the site the admin's account is of (the admin has no list of sites, as
// on the settings page); then the limits every layer is held to (docs/domains/routing.md「路由预设」).
export default function RoutingPresetsPage() {
  const { t } = useTranslation();
  const owner = useQuery(accountQuery(useSession().account));
  const platform = useQuery(layerPresetsQuery({ scope: "platform" }));
  // A node of a release before these operations answers 404. DataState would call that a missing page; say what it is.
  if (platform.error instanceof ApiError && platform.error.status === 404) {
    return (
      <Frame>
        <Panel title={t("routingPresets.unavailableTitle")}>
          <p className="px-4 py-3 text-ui text-t2">{t("routingPresets.unavailableBody")}</p>
        </Panel>
      </Frame>
    );
  }
  return (
    <Frame>
      {/* A new platform preset's prices start in the admin's site's currency, the one an operator most likely means. */}
      <Layer
        layer={{ scope: "platform" }}
        query={platform}
        currency={owner.data?.balance.balance.currency}
        platform={[]}
      />
      <DataState query={owner}>
        {(account) => (
          <SiteLayer
            siteId={account.site_id}
            currency={account.balance.balance.currency}
            platform={platform.data?.data ?? []}
          />
        )}
      </DataState>
      <RoutingLimits />
    </Frame>
  );
}

function Frame({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  return (
    <>
      <PageHeader title={t("routingPresets.title")} description={t("routingPresets.description")} />
      <PageBody>{children}</PageBody>
    </>
  );
}

// SiteLayer is the site's layer. Its presets are priced in the site's currency, and its default may also point at a
// platform preset.
function SiteLayer({
  siteId,
  currency,
  platform,
}: {
  siteId: AdminSchemas["SiteId"];
  currency: string;
  platform: Preset[];
}) {
  const layer: PresetLayer = { scope: "site", siteId };
  const query = useQuery(layerPresetsQuery(layer));
  return <Layer layer={layer} query={query} currency={currency} platform={platform} />;
}

// Layer is one layer's default for new keys and its presets. currency is the site's: a site preset's prices are in
// it, and a platform preset's start in it.
function Layer({
  layer,
  query,
  currency,
  platform,
}: {
  layer: PresetLayer;
  query: UseQueryResult<AdminSchemas["RoutingPresetList"]>;
  currency?: string;
  platform: Preset[];
}) {
  const { t } = useTranslation();
  const manage = can(useSession(), "sites.manage");
  const [open, setOpen] = useState<Open>(null);
  const presets = query.data?.data ?? [];
  const chosen = open?.kind === "create" ? undefined : presets.find((preset) => preset.id === open?.id);
  const close = () => setOpen(null);
  const create = manage ? (
    <Button variant="primary" size="md" onClick={() => setOpen({ kind: "create" })}>
      {t("routingPresets.create")}
    </Button>
  ) : undefined;
  return (
    <>
      <DefaultCard layer={layer} presets={[...presets, ...platform]} currency={currency} manage={manage} />
      <Panel title={t(`routingPresets.layer.${layer.scope}`)} action={create}>
        <DataState
          query={query}
          isEmpty={(data) => data.data.length === 0}
          empty={<EmptyState title={t("routingPresets.empty")} />}
        >
          {(data) => <PresetsTable presets={data.data} manage={manage} onOpen={setOpen} />}
        </DataState>
      </Panel>
      {open?.kind === "create" ? <PresetDialog layer={layer} currency={currency} onClose={close} /> : null}
      {open?.kind === "edit" && chosen ? (
        <PresetDialog key={chosen.id} layer={layer} preset={chosen} currency={currency} onClose={close} />
      ) : null}
      {open?.kind === "disable" && chosen ? (
        <DisablePresetDialog key={chosen.id} preset={chosen} onClose={close} />
      ) : null}
    </>
  );
}

function PresetsTable({
  presets,
  manage,
  onOpen,
}: {
  presets: Preset[];
  manage: boolean;
  onOpen: (open: Open) => void;
}) {
  const { t } = useTranslation();
  return (
    <Table label={t("routingPresets.title")}>
      <Head
        cols={[
          [t("routingPresets.col.name")],
          [t("routingPresets.col.rules"), "hidden md:table-cell"],
          [t("routingPresets.col.keys"), "hidden sm:table-cell"],
          [t("routingPresets.col.version"), "hidden lg:table-cell"],
        ]}
        action={manage}
      />
      <tbody>
        {presets.map((preset) => (
          <PresetRow key={preset.id} preset={preset} manage={manage} onOpen={onOpen} />
        ))}
      </tbody>
    </Table>
  );
}

function PresetRow({ preset, manage, onOpen }: { preset: Preset; manage: boolean; onOpen: (open: Open) => void }) {
  const { t } = useTranslation();
  // The system preset is the one every unbound key follows; it is not changed, and it counts no keys.
  const fixed = preset.id === systemPreset || preset.disabled;
  const keys = <Keys preset={preset} />;
  return (
    <tr>
      {/* On a phone the name takes the width of the hidden columns: the rules and the keys sit under it, and it wraps. */}
      <Td className="w-full max-w-0 space-y-0.5 md:w-auto md:max-w-72 md:min-w-40">
        <p className="text-t1 [overflow-wrap:anywhere]">{preset.name}</p>
        <Code>{preset.slug}</Code>
        <div className="flex flex-wrap gap-1 pt-0.5">
          {preset.id === systemPreset ? <Badge tone="neutral">{t("routingPresets.system")}</Badge> : null}
          {preset.disabled ? <Badge tone="warn">{t("routingPresets.disabled")}</Badge> : null}
          {preset.currency ? <Badge tone="neutral">{preset.currency}</Badge> : null}
        </div>
        <div className="pt-1 text-label md:hidden">
          <RulesSummary rules={preset.rules} />
          <p className="text-t3 sm:hidden">
            <Keys preset={preset} labelled />
          </p>
        </div>
      </Td>
      <Td className="hidden md:table-cell">
        <RulesSummary rules={preset.rules} />
      </Td>
      <Td className="hidden whitespace-nowrap tabular-nums sm:table-cell">{keys}</Td>
      <Td className="hidden tabular-nums lg:table-cell">{preset.version}</Td>
      {manage ? (
        <Td className="w-0 whitespace-nowrap">
          {fixed ? null : (
            <div className="flex justify-end gap-2">
              {(["edit", "disable"] as const).map((kind) => (
                <Button
                  key={kind}
                  size="sm"
                  aria-label={t(`routingPresets.${kind}Named`, { name: preset.name })}
                  onClick={() => onOpen({ kind, id: preset.id })}
                >
                  {t(`routingPresets.${kind}`)}
                </Button>
              ))}
            </div>
          )}
        </Td>
      ) : null}
    </tr>
  );
}

// Keys is how many keys of every account are bound to the preset. A node that does not count them, and the system
// preset, give no number: that is said, not shown as none. labelled adds the unit where the column header is hidden.
function Keys({ preset, labelled = false }: { preset: Preset; labelled?: boolean }) {
  const { t } = useTranslation();
  const format = useFormat();
  if (preset.id === systemPreset) {
    return t("routingPresets.keys.system");
  }
  if (preset.keys === undefined) {
    return t("routingPresets.keys.unknown");
  }
  return labelled
    ? t("routingPresets.keys.count", { count: preset.keys, keys: format.number(preset.keys) })
    : format.number(preset.keys);
}
