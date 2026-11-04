import { ApiError, consoleApi, type Schemas, unwrap } from "@axiom/api";
import { can, useErrors, useSession } from "@axiom/app";
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
} from "@axiom/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { PresetOptions } from "../key-form";
import { DisablePresetDialog, PresetDialog, type PresetEdit } from "../preset-dialogs";
import { type Sort, sorts, summaryOf } from "../preset-rules";
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
                defaultId={defaults.data?.preset_id}
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

// DefaultForm edits from the default it first read: the choice and the version it is sent at are one base, which a
// background read never moves, so a change made elsewhere meanwhile is refused (stale_revision) instead of
// overwritten. A saved default becomes the new base; after a refusal the user reloads, which takes the latest default
// as the base and drops the choice made against the old one.
function DefaultForm({
  first,
  presets,
  manage,
}: {
  first: Schemas["RoutingDefault"];
  presets: Preset[];
  manage: boolean;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const queryClient = useQueryClient();
  const [base, setBase] = useState(first);
  const [chosen, setChosen] = useState(first.preset_id ?? "");
  const rebase = (next: Schemas["RoutingDefault"]) => {
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
  const reload = async () => {
    const latest = await queryClient.fetchQuery({ ...routingDefaultQuery, staleTime: 0 });
    rebase(latest);
    save.reset();
  };
  const stale = save.error instanceof ApiError && save.error.code === "stale_revision";
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
        <div className="flex flex-wrap items-end gap-3">
          <DefaultSelect presets={presets} chosen={chosen} manage={manage} onChoose={setChosen} />
          {manage && stale ? (
            <Button onClick={() => void reload()}>{t("routingPresets.defaults.reload")}</Button>
          ) : null}
          {manage && !stale ? (
            <SubmitButton pending={save.isPending} disabled={chosen === (base.preset_id ?? "")}>
              {t("routingPresets.defaults.save")}
            </SubmitButton>
          ) : null}
        </div>
        {save.isSuccess ? (
          <p role="status" className="text-label text-ok">
            {t("routingPresets.defaults.saved")}
          </p>
        ) : null}
      </form>
    </Panel>
  );
}

function DefaultSelect({
  presets,
  chosen,
  manage,
  onChoose,
}: {
  presets: Preset[];
  chosen: string;
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
      <option value="">{t("routingPresets.defaults.none")}</option>
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
          <Th>{t("routingPresets.col.layer")}</Th>
          <Th className="hidden md:table-cell">{t("routingPresets.col.rules")}</Th>
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

function RulesSummary({ rules }: { rules: Schemas["RoutingRules"] }) {
  const { t } = useTranslation();
  const summary = summaryOf(rules);
  // A sort a newer node added has no words here yet; its name is shown as it is.
  const known = (sorts as readonly string[]).includes(summary.sort);
  const parts = [
    known ? t(`routingPresets.editor.sorts.${summary.sort as Sort}`) : summary.sort,
    summary.classes ? summary.classes.join(", ") : t("routingPresets.summary.allClasses"),
    summary.verifiedOnly ? t("routingPresets.summary.verified") : null,
    summary.priced ? t("routingPresets.summary.priced") : null,
  ].filter(Boolean);
  return (
    <>
      <p className="[overflow-wrap:anywhere] text-t2">{parts.join(" · ")}</p>
      {summary.overrides > 0 ? (
        <p className="text-label text-t4">{t("routingPresets.summary.overrides", { count: summary.overrides })}</p>
      ) : null}
    </>
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
  return (
    <tr>
      <Td className="w-full max-w-0 sm:w-auto sm:max-w-64 sm:min-w-40">
        <p className="truncate text-t1" title={preset.name}>
          {preset.name}
        </p>
        <p className="truncate font-mono text-label text-t4">{preset.slug}</p>
        {isDefault ? <Badge tone="ok">{t("routingPresets.isDefault")}</Badge> : null}
      </Td>
      <Td className="whitespace-nowrap">{t(`routingPresets.layer.${preset.scope}`)}</Td>
      <Td className="hidden md:table-cell">
        <RulesSummary rules={preset.rules} />
      </Td>
      <Td className="hidden tabular-nums lg:table-cell">{preset.version}</Td>
      {manage ? (
        <Td className="whitespace-nowrap">
          <PresetActions preset={preset} onOpen={onOpen} />
        </Td>
      ) : null}
    </tr>
  );
}

// Every preset can be copied into one of the account's; only the account's own can be changed or disabled.
function PresetActions({ preset, onOpen }: { preset: Preset; onOpen: (dialog: Dialog) => void }) {
  const { t } = useTranslation();
  const own = preset.scope === "account";
  return (
    <>
      <Button
        variant="ghost"
        size="icon"
        aria-label={t("routingPresets.copyNamed", { name: preset.name })}
        onClick={() => onOpen({ kind: "copy", preset })}
      >
        <Icon name="copy" />
      </Button>
      {own ? (
        <>
          <Button
            variant="ghost"
            size="icon"
            aria-label={t("routingPresets.editNamed", { name: preset.name })}
            onClick={() => onOpen({ kind: "edit", preset })}
          >
            <Icon name="pencil" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            aria-label={t("routingPresets.disableNamed", { name: preset.name })}
            onClick={() => onOpen({ kind: "disable", preset })}
          >
            <Icon name="trash" />
          </Button>
        </>
      ) : null}
    </>
  );
}
