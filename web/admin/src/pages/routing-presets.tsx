import { type AdminSchemas, ApiError } from "@axiom/api";
import { can, RulesSummary, systemPreset, useSession } from "@axiom/app";
import {
  ActionButton,
  Card,
  Cluster,
  DataState,
  EmptyState,
  PageHead,
  Stack,
  Table,
  Tag,
  Td,
  Text,
  useFormat,
} from "@axiom/ui";
import { type UseQueryResult, useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Code, Head, PageBody } from "../parts";
import { DefaultCard, DisablePresetDialog, PresetDialog } from "../preset-dialogs";
import { accountQuery, layerPresetsQuery, type PresetLayer } from "../queries";
import { RoutingLimits } from "../routing-limits";
import { RoutingSuspension, SuspendedNotice, usePresetsHold } from "../routing-suspension";

type Preset = AdminSchemas["RoutingPreset"];
// The dialog open on a layer: a new preset, or a change to one, followed by ID in the list so a refresh after a stale
// revision hands it the revision it must now send.
type Open = { kind: "create" } | { kind: "edit" | "disable"; id: string } | null;

// RoutingPresetsPage manages the two layers above the accounts: the platform's presets and default, which every site's
// keys may use, and the presets and default of the site the admin's account is of (the admin has no list of sites, as
// on the settings page); then the limits every layer is held to and the platform's switch that suspends every preset
// but the system's (docs/domains/routing.md「路由预设」).
export default function RoutingPresetsPage() {
  const { t } = useTranslation();
  const owner = useQuery(accountQuery(useSession().account));
  const platform = useQuery(layerPresetsQuery({ scope: "platform" }));
  // A node of a release before these operations answers 404. DataState would call that a missing page; say what it is.
  if (platform.error instanceof ApiError && platform.error.status === 404) {
    return (
      <Frame>
        <Card title={t("routingPresets.unavailableTitle")}>
          <Text tone="muted">{t("routingPresets.unavailableBody")}</Text>
        </Card>
      </Frame>
    );
  }
  return (
    <Frame>
      <SuspendedNotice />
      {/* A new platform preset's prices start in the admin's site's currency, the one an operator most likely means. */}
      <Layer
        layer={{ scope: "platform" }}
        query={platform}
        currency={owner.data?.balance.balance.currency}
        platform={[]}
        siteId={owner.data?.site_id}
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
      <RoutingSuspension />
    </Frame>
  );
}

function Frame({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  return (
    <>
      <PageHead title={t("routingPresets.title")} lede={t("routingPresets.description")} />
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
  return <Layer layer={layer} query={query} currency={currency} platform={platform} siteId={siteId} />;
}

// Layer is one layer's default for new keys and its presets. currency and siteId are the admin's site's: a site
// preset's prices are in its currency, and a platform preset's start in it; its default may point at either layer's.
function Layer({
  layer,
  query,
  currency,
  platform,
  siteId,
}: {
  layer: PresetLayer;
  query: UseQueryResult<AdminSchemas["RoutingPresetList"]>;
  currency?: string;
  platform: Preset[];
  siteId?: AdminSchemas["SiteId"];
}) {
  const { t } = useTranslation();
  const manage = can(useSession(), "sites.manage");
  const [open, setOpen] = useState<Open>(null);
  const hold = usePresetsHold();
  const presets = query.data?.data ?? [];
  const chosen = open?.kind === "create" ? undefined : presets.find((preset) => preset.id === open?.id);
  const close = () => setOpen(null);
  const create = manage ? (
    <ActionButton variant="pri" onClick={() => setOpen({ kind: "create" })} {...hold}>
      {t("routingPresets.create")}
    </ActionButton>
  ) : undefined;
  return (
    <>
      <DefaultCard layer={layer} presets={[...presets, ...platform]} currency={currency} manage={manage} />
      <Card layout="flush" title={t(`routingPresets.layer.${layer.scope}`)} actions={create}>
        <DataState
          query={query}
          isEmpty={(data) => data.data.length === 0}
          empty={<EmptyState title={t("routingPresets.empty")} />}
        >
          {(data) => <PresetsTable presets={data.data} manage={manage} onOpen={setOpen} />}
        </DataState>
      </Card>
      {open?.kind === "create" ? <PresetDialog layer={layer} currency={currency} onClose={close} /> : null}
      {open?.kind === "edit" && chosen ? (
        <PresetDialog key={chosen.id} layer={layer} preset={chosen} currency={currency} onClose={close} />
      ) : null}
      {open?.kind === "disable" && chosen ? (
        <DisablePresetDialog key={chosen.id} preset={chosen} siteId={siteId} onClose={close} />
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
          [t("routingPresets.col.name"), { main: true }],
          [t("routingPresets.col.rules"), { main: true }],
          [t("routingPresets.col.keys"), { align: "end" }],
          [t("routingPresets.col.version"), { align: "end" }],
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
  const hold = usePresetsHold();
  // The system preset is the one every unbound key follows; it is not changed, and it counts no keys.
  const fixed = preset.id === systemPreset || preset.disabled;
  const keys = <Keys preset={preset} />;
  return (
    <tr>
      <Td main>
        <Stack gap={4}>
          <Text as="span">{preset.name}</Text>
          <Code>{preset.slug}</Code>
          <Cluster gap={4}>
            {preset.id === systemPreset ? <Tag tone="neutral">{t("routingPresets.system")}</Tag> : null}
            {preset.disabled ? <Tag tone="warn">{t("routingPresets.disabled")}</Tag> : null}
            {preset.currency ? <Tag tone="neutral">{preset.currency}</Tag> : null}
          </Cluster>
        </Stack>
      </Td>
      <Td main>
        <RulesSummary summary={preset.summary} />
      </Td>
      <Td align="end">{keys}</Td>
      <Td align="end">{preset.version}</Td>
      {manage ? (
        <Td actions>
          {fixed ? null : (
            <Cluster justify="end">
              {(["edit", "disable"] as const).map((kind) => (
                <ActionButton
                  size="sm"
                  key={kind}
                  aria-label={t(`routingPresets.${kind}Named`, { name: preset.name })}
                  onClick={() => onOpen({ kind, id: preset.id })}
                  {...hold}
                >
                  {t(`routingPresets.${kind}`)}
                </ActionButton>
              ))}
            </Cluster>
          )}
        </Td>
      ) : null}
    </tr>
  );
}

// Keys is how many keys of every account are bound to the preset. A node that does not count them, and the system
// preset, give no number: that is said, not shown as none.
function Keys({ preset }: { preset: Preset }) {
  const { t } = useTranslation();
  const format = useFormat();
  if (preset.id === systemPreset) {
    return t("routingPresets.keys.system");
  }
  if (preset.keys === undefined) {
    return t("routingPresets.keys.unknown");
  }
  return format.number(preset.keys);
}

export { default as messages } from "../locales/settings";
