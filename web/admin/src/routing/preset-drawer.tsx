import type { AdminSchemas } from "@axiom/api";
import { RulesSummary, systemPreset } from "@axiom/app";
import { ActionButton, Cluster, Facts, Note, SidePanel, Stack } from "@axiom/ui";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, useSnapshot } from "../change";
import type { Lines } from "../lines";
import { Code } from "../parts";
import { DisablePresetDialog, PresetDialog, savePreset, useExplain } from "../preset-dialogs";
import type { PresetLayer } from "../queries";
import { usePresetsHold } from "../routing-suspension";
import { withOrder } from "./line-order";
import { Order } from "./order";
import { type Found, useOrderEdit } from "./order-edit";
import { PresetKeys } from "./preset-keys";

type Preset = AdminSchemas["RoutingPreset"];

type Open = "save" | "rules" | "disable" | null;

// PresetDrawer is a preset over the list (adP_preset in admin_pages_b.js). A platform preset's line order is edited in
// place: up, down, take away, add. Only a change enables 「保存为第 N 版」, which says the order before and after, and
// 「检查可用线路」 asks, before anything is saved, which lines cannot take a request now. The rest of the rules are
// edited in their own dialog, which keeps what it does not show.
export function PresetDrawer({
  preset,
  layer,
  currency,
  siteId,
  lines,
  manage,
  index,
  count,
  onStep,
  onClose,
}: {
  preset: Preset;
  layer: PresetLayer;
  currency?: string;
  siteId?: AdminSchemas["SiteId"];
  lines: Lines;
  manage: boolean;
  index: number;
  count: number;
  onStep: (by: -1 | 1) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const edit = useOrderEdit(preset, layer, currency, lines);
  const [opened, setOpened] = useState<{ id: string; open: Exclude<Open, null> }>();
  const open = opened?.id === preset.id ? opened.open : null;
  const fixed = preset.id === systemPreset || preset.disabled;
  const editable = manage && layer.scope === "platform" && !fixed;
  return (
    <>
      <SidePanel
        drawer
        wide
        title={preset.name}
        subtitle={`${preset.slug} · ${t(`routingPresets.scope.${layer.scope}`)} · ${t("routingPresets.version", { version: preset.version })}`}
        onClose={onClose}
        steps={{
          index,
          count,
          onStep,
          previousLabel: t("routingPresets.drawer.previous"),
          nextLabel: t("routingPresets.drawer.next"),
        }}
        footer={
          manage && !fixed ? (
            <Footer
              editable={editable}
              dirty={edit.dirty}
              version={preset.version + 1}
              checking={edit.check.isPending}
              onOpen={(open) => setOpened({ id: preset.id, open })}
              onCheck={() => edit.check.mutate()}
            />
          ) : undefined
        }
      >
        <Body preset={preset} edit={edit} lines={lines} editable={editable} />
      </SidePanel>
      {open ? (
        <Dialogs
          open={open}
          preset={preset}
          layer={layer}
          currency={currency}
          siteId={siteId}
          saved={edit.saved}
          order={edit.order}
          names={edit.names}
          onClose={() => setOpened(undefined)}
        />
      ) : null}
    </>
  );
}

// SaveOrder writes the order as the preset's next version, as of the revision the drawer read. The inverse is the
// order it had, as a version of its own.
function SaveOrder({
  preset: live,
  layer,
  saved,
  order,
  names,
  onClose,
}: {
  preset: Preset;
  layer: PresetLayer;
  saved: readonly string[];
  order: readonly string[];
  names: (codes: readonly string[]) => string[];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const explain = useExplain();
  const [preset, reload] = useSnapshot(live);
  const input = (rules: Preset["rules"], of: Preset) => ({
    slug: of.slug,
    name: of.name,
    currency: of.currency,
    rules,
  });
  return (
    <ChangeDialog<readonly string[], Preset>
      operation={`routing-preset-order:${preset.id}`}
      title={t("routingPresets.order.save", { version: preset.version + 1 })}
      diff={[
        { label: t("routingPresets.order.title"), before: names(saved).join(" → "), after: names(order).join(" → ") },
      ]}
      warn={preset.keys ? t("routingPresets.order.live", { count: preset.keys }) : undefined}
      action={t("routingPresets.form.save")}
      reload={reload}
      explain={explain}
      read={() => order}
      send={(next, { reason, key }) =>
        savePreset(layer, preset, input(withOrder(preset.rules, next), preset), reason, key)
      }
      done={(changed) => <p>{t("routingPresets.saved", { name: changed.name })}</p>}
      toast={(changed) => t("routingPresets.order.toast", { name: changed.name, version: changed.version })}
      undo={(changed, { reason, key }) =>
        savePreset(layer, changed, input(withOrder(changed.rules, saved), changed), reason, key)
      }
      onClose={onClose}
    />
  );
}

function Footer({
  editable,
  dirty,
  version,
  checking,
  onOpen,
  onCheck,
}: {
  editable: boolean;
  dirty: boolean;
  version: number;
  checking: boolean;
  onOpen: (open: Exclude<Open, null>) => void;
  onCheck: () => void;
}) {
  const { t } = useTranslation();
  const hold = usePresetsHold();
  return (
    <Cluster justify="end">
      <ActionButton variant="gho" onClick={() => onOpen("disable")} {...hold}>
        {t("routingPresets.disable")}
      </ActionButton>
      <ActionButton onClick={() => onOpen("rules")} {...hold}>
        {t("routingPresets.order.rules")}
      </ActionButton>
      {editable ? (
        <>
          <ActionButton icon="check" pending={checking} onClick={onCheck}>
            {t("routingPresets.order.check")}
          </ActionButton>
          <ActionButton
            variant="pri"
            disabled={!dirty || hold.disabled}
            title={hold.title}
            onClick={() => onOpen("save")}
          >
            {t("routingPresets.order.save", { version })}
          </ActionButton>
        </>
      ) : null}
    </Cluster>
  );
}

function CheckNote({ found }: { found: Found }) {
  const { t } = useTranslation();
  const detail = [
    found.down.length > 0 ? t("routingPresets.order.brokenDetail") : null,
    found.unmatched > 0 ? t("routingPresets.order.unmatched", { count: found.unmatched }) : null,
  ].filter((part) => part !== null);
  return (
    <Note
      title={
        found.down.length > 0
          ? t("routingPresets.order.broken", { count: found.down.length })
          : t("routingPresets.order.allUp")
      }
    >
      {detail.join(" ")}
    </Note>
  );
}

function Dialogs({
  open,
  preset,
  layer,
  currency,
  siteId,
  saved,
  order,
  names,
  onClose,
}: {
  open: Exclude<Open, null>;
  preset: Preset;
  layer: PresetLayer;
  currency?: string;
  siteId?: AdminSchemas["SiteId"];
  saved: readonly string[];
  order: readonly string[];
  names: (codes: readonly string[]) => string[];
  onClose: () => void;
}) {
  switch (open) {
    case "save":
      return <SaveOrder preset={preset} layer={layer} saved={saved} order={order} names={names} onClose={onClose} />;
    case "rules":
      return <PresetDialog layer={layer} preset={preset} currency={currency} onClose={onClose} />;
    default:
      return <DisablePresetDialog preset={preset} siteId={siteId} onClose={onClose} />;
  }
}

function Body({
  preset,
  edit,
  lines,
  editable,
}: {
  preset: Preset;
  edit: ReturnType<typeof useOrderEdit>;
  lines: Lines;
  editable: boolean;
}) {
  const { t } = useTranslation();
  const hold = usePresetsHold();
  return (
    <Stack>
      {edit.found ? <CheckNote found={edit.found} /> : null}
      <Facts
        rows={[
          [t("routingPresets.col.rules"), <RulesSummary key="rules" summary={preset.summary} />],
          ...(preset.currency
            ? ([[t("routingPresets.form.currency"), <Code key="currency">{preset.currency}</Code>]] as const)
            : []),
          [t("routingPresets.col.keys"), <PresetKeys key="keys" preset={preset} />],
        ]}
      />
      <Order
        order={edit.order}
        lines={lines}
        editable={editable}
        free={edit.free}
        onChange={edit.set}
        hold={hold.disabled === true}
      />
    </Stack>
  );
}
