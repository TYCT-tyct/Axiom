import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { DataState } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { DefaultCard } from "../preset-dialogs";
import { accountQuery, layerPresetsQuery, type PresetLayer } from "../queries";
import { RoutingLimits } from "../routing-limits";
import { RoutingSuspension, SuspendedNotice } from "../routing-suspension";

// DefaultsTab is what the layers hold together: the preset each layer's new keys take, the switch that suspends every
// preset but the system's, and how many presets each layer may have (docs/domains/routing.md「路由预设」).
export function DefaultsTab() {
  const owner = useQuery(accountQuery(useSession().account));
  return (
    <DataState query={owner}>
      {(account) => <Layers siteId={account.site_id} currency={account.balance.balance.currency} />}
    </DataState>
  );
}

function Layers({ siteId, currency }: { siteId: AdminSchemas["SiteId"]; currency: string }) {
  const manage = can(useSession(), "sites.manage");
  const site: PresetLayer = { scope: "site", siteId };
  const platform = useQuery(layerPresetsQuery({ scope: "platform" })).data?.data ?? [];
  const own = useQuery(layerPresetsQuery(site)).data?.data ?? [];
  return (
    <>
      <SuspendedNotice />
      <DefaultCard layer={{ scope: "platform" }} presets={platform} currency={currency} manage={manage} />
      <DefaultCard layer={site} presets={[...own, ...platform]} currency={currency} manage={manage} />
      <RoutingSuspension />
      <RoutingLimits />
    </>
  );
}
