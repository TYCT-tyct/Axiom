import { ApiError, type Schemas } from "@axiom/api";
import { can, summaryLine, useSession } from "@axiom/app";
import Wide from "@axiom/boards/AxApp/regions/Route";
import wideEn from "@axiom/boards/AxApp/regions/Route.en";
import wideZh from "@axiom/boards/AxApp/regions/Route.zh";
import Phone from "@axiom/boards/AxAppPhone/regions/Route";
import phoneEn from "@axiom/boards/AxAppPhone/regions/Route.en";
import phoneZh from "@axiom/boards/AxAppPhone/regions/Route.zh";
import { Button, Icon, useFormat, usePageTitle } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { type SyntheticEvent, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ListStates, pressKey, useWide } from "../board";
import { PresetDialog } from "../preset-dialogs";
import { routingDefaultQuery, routingPresetsQuery } from "../queries";
import { Defaults, PresetActionsDialog } from "./routing-presets-actions";

type Preset = Schemas["RoutingPreset"];
type Props = Parameters<typeof Wide>[0];
type Row = Props["rows"]["presets0"][number];
type Format = ReturnType<typeof useFormat>;

// RoutingPresets is the board's Routing page (#747): the presets a key of the account may be bound to, the widest layer
// first (docs/domains/routing.md「路由预设」). A row's name opens the preset, which the design has no editor for
// (console-gap L2), and the account's default for new keys is set from there too.
export default function RoutingPresets() {
  const { t, i18n } = useTranslation();
  // The rules summary's words are the common namespace's, which the editor and the detail share.
  const { t: common } = useTranslation("common");
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
  // A session that loses `api_keys.view`, or a node that then refuses the read, keeps what the query cache holds: the
  // page shows its states instead of the rows, offers nothing to make, and closes a preset it was showing. A read that
  // merely failed keeps the page as it is — a draft open in its dialog is the person's — and the list offers a retry.
  const refused = presets.error instanceof ApiError && presets.error.status === 403;
  const permitted = view && !refused;
  const rows = (permitted && !presets.isError ? (presets.data?.data ?? []) : []).map((preset) =>
    row(preset, preset.id === newKeysPreset(defaults.data), t, common, format),
  );
  useEffect(() => {
    if (!permitted) {
      setOpen(null);
    }
  }, [permitted]);
  const values = {
    openPreset: (event: SyntheticEvent) =>
      setOpen(presetOf(rows.length ? presets.data?.data : undefined, event) ?? null),
    press: pressKey,
  };
  const text = labels(t);
  const props = {
    values,
    text,
    rows: { presets0: rows },
    description: t("routingPresets.description"),
    create: <CreatePreset manage={manage} permitted={permitted} onOpen={() => setOpen("create")} />,
  };
  return (
    <>
      <ListStates query={presets} view={view} ns="routingPresets" />
      <EmptyNote presets={presets.data?.data} permitted={permitted} />
      {wide ? (
        <Wide t={(key) => (english ? wideEn : wideZh)[key]} {...props} />
      ) : (
        <Phone t={(key) => (english ? phoneEn : phoneZh)[key]} {...props} />
      )}
      {permitted ? <Opened open={open} onClose={() => setOpen(null)} /> : null}
    </>
  );
}

// CreatePreset is the page's action, where the board leaves the page's actions empty: only someone who may change
// presets and may read them at all gets it.
function CreatePreset({ manage, permitted, onOpen }: { manage: boolean; permitted: boolean; onOpen: () => void }) {
  const { t } = useTranslation();
  if (!manage || !permitted) {
    return null;
  }
  return (
    <Button variant="primary" size="md" onClick={onOpen}>
      <Icon name="plus" />
      {t("routingPresets.create")}
    </Button>
  );
}

// EmptyNote is what an account with no preset yet still needs: what a key made now takes, and whether it is refused.
function EmptyNote({ presets, permitted }: { presets: Preset[] | undefined; permitted: boolean }) {
  if (!permitted || presets?.length !== 0) {
    return null;
  }
  return <Defaults />;
}

// Opened is the preset a row's name opened, or the console's create dialog; the board draws neither (console-gap L2).
function Opened({ open, onClose }: { open: Preset | "create" | null; onClose: () => void }) {
  if (open === "create") {
    return <PresetDialog edit={{ kind: "create" }} onClose={onClose} />;
  }
  return open ? <PresetActionsDialog preset={open} onClose={onClose} /> : null;
}

// labels are the board's texts in the console's words: the page's name, what the list holds, and its columns.
function labels(t: TFunction) {
  return {
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
function row(preset: Preset, isDefault: boolean, t: TFunction, common: TFunction<"common">, format: Format): Row {
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
    f14: summaryLine(common, preset.summary),
    f15: "r num",
    f16: preset.keys === undefined ? "—" : format.number(preset.keys),
    f17: "r",
    f18: format.number(preset.version),
  };
}
