import { ApiError, consoleApi, type Schemas, unwrap } from "@axiom/api";
import { can, RulesSummary, useErrors, useSession } from "@axiom/app";
import {
  Badge,
  Button,
  DataState,
  EmptyState,
  FormError,
  Icon,
  PageHeader,
  Panel,
  SelectField,
  SubmitButton,
  Table,
  Td,
  Th,
  useFormat,
} from "@axiom/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { PresetOptions, useNewKeys } from "../key-form";
import { DisablePresetDialog, PresetDialog, type PresetEdit } from "../preset-dialogs";
import { routingDefaultQuery, routingPresetsQuery } from "../queries";

type Preset = Schemas["RoutingPreset"];
type Dialog = PresetEdit | { kind: "disable"; preset: Preset };

// RoutingPresets lists the presets the account's keys may use (the platform's, the site's and its own), lets it make,
// copy, change and disable its own, and sets the preset its new keys take (docs/domains/routing.md「路由预设」).
export default function RoutingPresets() {
  const { t } = useTranslation();
  const manage = can(useSession(), "api_keys.manage");
  const presets = useQuery(routingPresetsQuery);
  const defaults = useQuery(routingDefaultQuery);
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const close = () => setDialog(null);
  return (
    <>
      <PageHeader title={t("nav.routingPresets")} description={t("routingPresets.description")} />
      <div className="space-y-4 p-4 sm:p-6">
        {presets.data ? <DefaultPanel presets={presets.data.data} manage={manage} /> : null}
        {manage ? (
          <Button variant="primary" size="md" onClick={() => setDialog({ kind: "create" })}>
            <Icon name="plus" />
            {t("routingPresets.create")}
          </Button>
        ) : null}
        <Panel>
          <DataState
            query={presets}
            isEmpty={(data) => data.data.length === 0}
            empty={<EmptyState title={t("routingPresets.empty.title")}>{t("routingPresets.empty.body")}</EmptyState>}
          >
            {(data) => (
              <PresetsTable
                presets={data.data}
                defaultId={newKeysPreset(defaults.data)}
                manage={manage}
                onOpen={setDialog}
              />
            )}
          </DataState>
        </Panel>
      </div>
      {dialog?.kind === "disable" ? <DisablePresetDialog preset={dialog.preset} onClose={close} /> : null}
      {dialog && dialog.kind !== "disable" ? <PresetDialog edit={dialog} onClose={close} /> : null}
    </>
  );
}

// newKeysPreset is the preset a key made now takes, which the list marks: the node names it, or names none because the
// default refuses new keys; only an older node, which says neither, leaves the account's own default to stand for it.
function newKeysPreset(read: Schemas["AccountRoutingDefault"] | undefined) {
  return read?.new_keys_refused ? undefined : (read?.new_keys_preset_id ?? read?.preset_id);
}

// DefaultPanel shows and sets the account's default for new keys. A later read that fails keeps the form and what the
// user chose in it; only a first read that fails says the default is unavailable.
function DefaultPanel({ presets, manage }: { presets: Preset[]; manage: boolean }) {
  const { t } = useTranslation();
  const defaults = useQuery(routingDefaultQuery);
  if (defaults.data) {
    return <DefaultForm first={defaults.data} presets={presets} manage={manage} />;
  }
  return defaults.isError ? (
    <Panel className="p-4">
      <p className="text-ui text-t2">{t("routingPresets.unavailable")}</p>
    </Panel>
  ) : null;
}

// useDefaultEdit edits from the default it first read: the choice and the version it is sent at are one base, which a
// background read never moves, so a change made elsewhere meanwhile is refused (stale_revision) instead of
// overwritten. A saved default becomes the new base; after a refusal the user reloads, which takes the latest default
// as the base and drops the choice made against the old one. A reload that fails keeps the old base and the refusal,
// so it can be tried again, and says why it failed.
function useDefaultEdit(first: Schemas["AccountRoutingDefault"]) {
  const queryClient = useQueryClient();
  const [base, setBase] = useState(first);
  const [chosen, setChosen] = useState(first.preset_id ?? "");
  const rebase = (next: Schemas["AccountRoutingDefault"]) => {
    setBase(next);
    setChosen(next.preset_id ?? "");
  };
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        consoleApi.PUT("/api/console/v1/routing-default", {
          body: { preset_id: chosen || null, version: base.version },
        }),
      ),
    onSuccess: (saved) => {
      rebase(saved);
      queryClient.setQueryData(routingDefaultQuery.queryKey, saved);
    },
  });
  const reload = useMutation({
    mutationFn: () => queryClient.fetchQuery({ ...routingDefaultQuery, staleTime: 0 }),
    onSuccess: (latest) => {
      rebase(latest);
      save.reset();
    },
  });
  const stale = save.error instanceof ApiError && save.error.code === "stale_revision";
  return { chosen, setChosen, unchanged: chosen === (base.preset_id ?? ""), save, reload, stale };
}

function DefaultForm({
  first,
  presets,
  manage,
}: {
  first: Schemas["AccountRoutingDefault"];
  presets: Preset[];
  manage: boolean;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const { chosen, setChosen, unchanged, save, reload, stale } = useDefaultEdit(first);
  // What the site's or the platform's default gives is known only while the account sets none: the node names what a
  // key made now takes, and with the account's own default that is the account's. The default workspace's own default
  // is not the site's or the platform's (NewKeysWarning says it), and the account's can point at no workspace's preset.
  const latest = useQuery(routingDefaultQuery).data;
  const newKeys = useNewKeys();
  const inherited = latest && !latest.preset_id && newKeys.layer !== "workspace" ? newKeys.name : undefined;
  const choosable = presets.filter((preset) => preset.scope !== "workspace");
  return (
    <Panel className="p-4">
      <form
        noValidate
        className="space-y-3"
        onSubmit={(event) => {
          event.preventDefault();
          if (!save.isPending && !stale) {
            save.mutate();
          }
        }}
      >
        <h2 className="text-ui font-medium text-t1">{t("routingPresets.defaults.title")}</h2>
        {save.isError ? <FormError>{errors.message(save.error)}</FormError> : null}
        {reload.isError ? <FormError>{errors.message(reload.error)}</FormError> : null}
        <div className="flex flex-wrap items-end gap-3">
          <DefaultSelect
            presets={choosable}
            chosen={chosen}
            inherited={inherited}
            manage={manage}
            onChoose={setChosen}
          />
          {manage ? (
            <DefaultAction
              stale={stale}
              reloading={reload.isPending}
              saving={save.isPending}
              unchanged={unchanged}
              onReload={() => reload.mutate()}
            />
          ) : null}
        </div>
        <NewKeysWarning newKeys={newKeys} />
        {save.isSuccess ? (
          <p role="status" className="text-label text-ok">
            {t("routingPresets.defaults.saved")}
          </p>
        ) : null}
      </form>
    </Panel>
  );
}

// NewKeysWarning says when the account's default does not decide what a key made now takes: the default refuses new
// keys, or the default workspace, where the console makes keys, sets its own default, which comes before the
// account's (new_keys_layer). An older node says neither, and nothing is said.
function NewKeysWarning({ newKeys }: { newKeys: ReturnType<typeof useNewKeys> }) {
  const { t } = useTranslation();
  const byWorkspace = newKeys.layer === "workspace";
  if (newKeys.refused) {
    return (
      <p className="text-label text-warn">
        {t(byWorkspace ? "routingPresets.defaults.refusedWorkspace" : "routingPresets.defaults.refused")}
      </p>
    );
  }
  return byWorkspace && newKeys.id ? (
    <p className="text-label text-warn [overflow-wrap:anywhere]">
      {t("routingPresets.defaults.workspace", { name: newKeys.name ?? newKeys.id })}
    </p>
  ) : null;
}

// DefaultAction saves the choice, or after a refusal loads the latest default, which is the only way on from there.
function DefaultAction({
  stale,
  reloading,
  saving,
  unchanged,
  onReload,
}: {
  stale: boolean;
  reloading: boolean;
  saving: boolean;
  unchanged: boolean;
  onReload: () => void;
}) {
  const { t } = useTranslation();
  return stale ? (
    <Button disabled={reloading} onClick={onReload}>
      {t("routingPresets.defaults.reload")}
    </Button>
  ) : (
    <SubmitButton pending={saving} disabled={unchanged}>
      {t("routingPresets.defaults.save")}
    </SubmitButton>
  );
}

function DefaultSelect({
  presets,
  chosen,
  inherited,
  manage,
  onChoose,
}: {
  presets: Preset[];
  chosen: string;
  inherited: string | undefined;
  manage: boolean;
  onChoose: (id: string) => void;
}) {
  const { t } = useTranslation();
  // The default always points at a live preset the account can use; one the list lacks is shown by its ID.
  const known = chosen === "" || presets.some((preset) => preset.id === chosen);
  return (
    <SelectField
      className="sm:w-80"
      label={t("routingPresets.defaults.label")}
      hint={t("routingPresets.defaults.hint")}
      value={chosen}
      disabled={!manage}
      onChange={(event) => onChoose(event.currentTarget.value)}
    >
      <option value="">
        {inherited ? t("routingPresets.defaults.noneNamed", { name: inherited }) : t("routingPresets.defaults.none")}
      </option>
      {known ? null : <option value={chosen}>{chosen}</option>}
      <PresetOptions presets={presets} />
    </SelectField>
  );
}

function PresetsTable({
  presets,
  defaultId,
  manage,
  onOpen,
}: {
  presets: Preset[];
  defaultId: string | undefined;
  manage: boolean;
  onOpen: (dialog: Dialog) => void;
}) {
  const { t } = useTranslation();
  return (
    <Table label={t("nav.routingPresets")}>
      <thead>
        <tr>
          <Th>{t("routingPresets.col.name")}</Th>
          <Th className="hidden sm:table-cell">{t("routingPresets.col.layer")}</Th>
          <Th className="hidden md:table-cell">{t("routingPresets.col.rules")}</Th>
          <Th className="hidden sm:table-cell">{t("routingPresets.col.keys")}</Th>
          <Th className="hidden lg:table-cell">{t("routingPresets.col.version")}</Th>
          {manage ? <Th className="w-0" /> : null}
        </tr>
      </thead>
      <tbody>
        {presets.map((preset) => (
          <PresetRow
            key={preset.id}
            preset={preset}
            isDefault={preset.id === defaultId}
            manage={manage}
            onOpen={onOpen}
          />
        ))}
      </tbody>
    </Table>
  );
}

function PresetRow({
  preset,
  isDefault,
  manage,
  onOpen,
}: {
  preset: Preset;
  isDefault: boolean;
  manage: boolean;
  onOpen: (dialog: Dialog) => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <tr>
      {/* On a phone the name takes the width the layer column would: the layer, and below md the rules summary, sit
          under it, and it wraps rather than truncates. */}
      <Td className="w-full max-w-0 space-y-0.5 md:w-auto md:max-w-64 md:min-w-40">
        <p className="text-t1 [overflow-wrap:anywhere]">{preset.name}</p>
        <p className="truncate font-mono text-label text-t4">{preset.slug}</p>
        {isDefault ? <Badge tone="ok">{t("routingPresets.isDefault")}</Badge> : null}
        <p className="text-label text-t3 sm:hidden">{t(`routingPresets.layer.${preset.scope}`)}</p>
        <div className="pt-1 text-label md:hidden">
          <RulesSummary rules={preset.rules} />
        </div>
      </Td>
      <Td className="hidden w-0 whitespace-nowrap sm:table-cell">{t(`routingPresets.layer.${preset.scope}`)}</Td>
      <Td className="hidden md:table-cell">
        <RulesSummary rules={preset.rules} />
      </Td>
      <Td className="hidden whitespace-nowrap tabular-nums sm:table-cell">
        {/* The account's own keys bound to it; a node that does not count them, and the system preset, give none. */}
        {preset.keys === undefined ? "—" : format.number(preset.keys)}
      </Td>
      <Td className="hidden tabular-nums lg:table-cell">{preset.version}</Td>
      {manage ? (
        <Td className="w-0 whitespace-nowrap">
          <PresetActions preset={preset} onOpen={onOpen} />
        </Td>
      ) : null}
    </tr>
  );
}

const actions = [
  { kind: "copy", icon: "copy" },
  { kind: "edit", icon: "pencil" },
  { kind: "disable", icon: "trash" },
] as const;

// Every preset can be copied into one of the account's; only the account's own can be changed or disabled. Each icon
// shows its verb on hover and on keyboard focus; its accessible name adds the preset's, as the rows' icons look alike.
// The tip sits to the button's start side, inside the row's height, so the table's scroll box never clips it.
function PresetActions({ preset, onOpen }: { preset: Preset; onOpen: (dialog: Dialog) => void }) {
  const { t } = useTranslation();
  const own = preset.scope === "account";
  return (
    <div className="flex justify-end">
      {actions
        .filter((action) => own || action.kind === "copy")
        .map(({ kind, icon }) => (
          <span key={kind} className="group relative">
            <Button
              variant="ghost"
              size="icon"
              aria-label={t(`routingPresets.${kind}Named`, { name: preset.name })}
              onClick={() => onOpen({ kind, preset })}
            >
              <Icon name={icon} />
            </Button>
            <span
              aria-hidden
              className="pointer-events-none absolute end-full top-1/2 z-10 me-1 -translate-y-1/2 rounded-xs bg-btn px-2 py-1 text-label whitespace-nowrap text-btn-ink opacity-0 transition-opacity duration-150 group-hover:opacity-100 group-has-[:focus-visible]:opacity-100"
            >
              {t(`routingPresets.actions.${kind}`)}
            </span>
          </span>
        ))}
    </div>
  );
}
