import type { Schemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import Wide from "@axiom/boards/AxApp/regions/Route";
import wideEn from "@axiom/boards/AxApp/regions/Route.en";
import wideZh from "@axiom/boards/AxApp/regions/Route.zh";
import Phone from "@axiom/boards/AxAppPhone/regions/Route";
import phoneEn from "@axiom/boards/AxAppPhone/regions/Route.en";
import phoneZh from "@axiom/boards/AxAppPhone/regions/Route.zh";
import { Button, Icon, useFormat, usePageTitle } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { type SyntheticEvent, useState } from "react";
import { useTranslation } from "react-i18next";
import { PresetDialog } from "../preset-dialogs";
import { routingDefaultQuery, routingPresetsQuery } from "../queries";
import { ListStates, pressKey, useWide } from "./board";
import { PresetActionsDialog } from "./routing-presets-actions";

type Preset = Schemas["RoutingPreset"];
type Summary = Schemas["RoutingRulesSummary"];
type Props = Parameters<typeof Wide>[0];
type Row = Props["rows"]["presets0"][number];
type Format = ReturnType<typeof useFormat>;

// The sorts the contract names, in the order the rules editor offers them.
const sorts = ["balanced", "price", "throughput", "latency", "quality"] as const;

// RoutingPresets is the board's Routing page (#747): the presets a key of the account may be bound to, the widest layer
// first (docs/domains/routing.md「路由预设」). A row's name opens the preset, which the design has no editor for
// (console-gap L2), and the account's default for new keys is set from there too.
export default function RoutingPresets() {
  const { t, i18n } = useTranslation();
  const session = useSession();
  const view = can(session, "api_keys.view");
  const manage = can(session, "api_keys.manage");
  const presets = useQuery({ ...routingPresetsQuery, enabled: view });
  const defaults = useQuery({ ...routingDefaultQuery, enabled: view });
  const [open, setOpen] = useState<Preset | "create" | null>(null);
  const wide = useWide();
  const format = useFormat();
  const english = (i18n.resolvedLanguage ?? i18n.language).startsWith("en");
  usePageTitle(t("nav.routingPresets"));
  const rows = (presets.data?.data ?? []).map((preset) =>
    row(preset, preset.id === newKeysPreset(defaults.data), t, format),
  );
  const values = {
    openPreset: (event: SyntheticEvent) => setOpen(presetOf(presets.data?.data, event) ?? null),
    press: pressKey,
  };
  const text = {
    title: t("nav.routingPresets"),
    notice: t("routingPresets.notice"),
    listTitle: t("routingPresets.list.title"),
    listHint: t("routingPresets.list.hint"),
    colName: t("routingPresets.col.name"),
    colLayer: t("routingPresets.col.layer"),
    colRules: t("routingPresets.col.rules"),
    colKeys: t("routingPresets.col.keys"),
    colVersion: t("routingPresets.col.version"),
  };
  const create = manage ? (
    <Button variant="primary" size="md" onClick={() => setOpen("create")}>
      <Icon name="plus" />
      {t("routingPresets.create")}
    </Button>
  ) : undefined;
  const props = {
    values,
    text,
    rows: { presets0: rows },
    description: t("routingPresets.description"),
    create,
  };
  return (
    <>
      <ListStates query={presets} allowed={view} ns="routingPresets" />
      {wide ? (
        <Wide t={(key) => (english ? wideEn : wideZh)[key]} {...props} />
      ) : (
        <Phone t={(key) => (english ? phoneEn : phoneZh)[key]} {...props} />
      )}
      {open === "create" ? <PresetDialog edit={{ kind: "create" }} onClose={() => setOpen(null)} /> : null}
      {open && open !== "create" ? <PresetActionsDialog preset={open} onClose={() => setOpen(null)} /> : null}
    </>
  );
}

// newKeysPreset is the preset a key made now takes: the node names it, or names none because the default refuses new
// keys; only an older node, which says neither, leaves the account's own default to stand for it.
function newKeysPreset(read: Schemas["AccountRoutingDefault"] | undefined) {
  return read?.new_keys_refused ? undefined : (read?.new_keys_preset_id ?? read?.preset_id);
}

// presetOf is the row an event came from: every repeated row carries its preset's id, which is what tells the rows of a
// table drawn from the board apart.
function presetOf(presets: Preset[] | undefined, event: SyntheticEvent) {
  const id = (event.currentTarget as HTMLElement).closest("tr")?.dataset.id;
  return presets?.find((preset) => preset.id === id);
}

// row is one preset as the board's table draws it: its name and slug, the layer it comes from, its rules in a line, the
// account's keys bound to it, and its rules' version.
function row(preset: Preset, isDefault: boolean, t: TFunction, format: Format): Row {
  return {
    key: preset.id,
    kind: "0",
    f0: preset.id,
    f1: isDefault ? "true" : "false",
    f2: "",
    f3: "qkn",
    f4: "button",
    f5: "0",
    f6: t("routingPresets.openNamed", { name: preset.name }),
    f7: preset.name,
    f8: "mut",
    f9: isDefault ? `${preset.slug} · ${t("routingPresets.isDefault")}` : preset.slug,
    f10: "",
    f11: "qsd ok",
    f12: t(`routingPresets.layer.${preset.scope}`),
    f13: "r num",
    f14: rulesLine(preset.summary, t),
    f15: "r num",
    f16: preset.keys === undefined ? "—" : format.number(preset.keys),
    f17: "r",
    f18: format.number(preset.version),
  };
}

// rulesLine is a preset's rules in one line, as the list shows them: the sort, the sources kept, the verified and price
// thresholds, and how many overrides follow. An older node, or rules this node cannot read, give none.
function rulesLine(summary: Summary | undefined, t: TFunction) {
  if (!summary) {
    return t("presetRules.summary.unavailable", { ns: "common" });
  }
  const parts = [
    // A sort a newer node added has no words here yet; its name is shown as it is.
    sorts.includes(summary.sort as (typeof sorts)[number])
      ? t(`presetRules.sorts.${summary.sort}`, { ns: "common" })
      : summary.sort,
    summary.classes ? summary.classes.join(", ") : t("presetRules.summary.allClasses", { ns: "common" }),
    summary.verified_only ? t("presetRules.summary.verified", { ns: "common" }) : null,
    summary.priced ? t("presetRules.summary.priced", { ns: "common" }) : null,
    summary.overrides > 0 ? t("presetRules.summary.overrides", { count: summary.overrides, ns: "common" }) : null,
  ].filter(Boolean);
  return parts.join(" · ");
}
