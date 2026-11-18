import { ApiError, consoleApi, type Schemas, unwrap } from "@axiom/api";
import { can, RulesSummary, useErrors, useSession } from "@axiom/app";
import { Button, Dialog, FormError } from "@axiom/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useNewKeys } from "../key-form";
import { DisablePresetDialog, PresetDialog, type PresetEdit } from "../preset-dialogs";
import { routingDefaultQuery } from "../queries";

type Preset = Schemas["RoutingPreset"];

// PresetActionsDialog is a preset row's ways on: make it the account's default for new keys or stop being it, open it to
// change it, copy it into one of the account's own, or disable it. The board's table draws a row's name as a button and
// nothing else, so what it cannot draw lives here; only the account's own presets (and its workspaces') can be changed
// or disabled.
export function PresetActionsDialog({ preset, onClose }: { preset: Preset; onClose: () => void }) {
  const { t } = useTranslation();
  const manage = can(useSession(), "api_keys.manage");
  const [next, setNext] = useState<PresetEdit["kind"] | "disable" | null>(null);
  if (next === "disable") {
    return <DisablePresetDialog preset={preset} onClose={onClose} />;
  }
  if (next) {
    return <PresetDialog edit={{ kind: next, preset }} onClose={onClose} />;
  }
  const own = preset.scope === "account" || preset.scope === "workspace";
  return (
    <Dialog title={preset.name} onClose={onClose}>
      <div className="space-y-4">
        <div className="text-label">
          <p className="font-mono text-t4">{preset.slug}</p>
          <RulesSummary summary={preset.summary} />
        </div>
        {manage ? <DefaultButton preset={preset} /> : null}
        <div className="flex flex-wrap gap-2">
          {!manage ? (
            <p className="text-label text-t3">{t("routingPresets.notice")}</p>
          ) : (
            <>
              <Button size="md" onClick={() => setNext(own ? "edit" : "copy")}>
                {t(own ? "routingPresets.actions.edit" : "routingPresets.actions.copy")}
              </Button>
              {own ? (
                <Button size="md" onClick={() => setNext("copy")}>
                  {t("routingPresets.actions.copy")}
                </Button>
              ) : null}
              {own ? (
                <Button size="md" variant="danger" onClick={() => setNext("disable")}>
                  {t("routingPresets.actions.disable")}
                </Button>
              ) : null}
            </>
          )}
        </div>
      </div>
    </Dialog>
  );
}

// DefaultButton points the account's default at this preset, or clears it when the preset already is the default. The
// version the page read goes with the change, so one made elsewhere meanwhile is refused (stale_revision) instead of
// overwritten; the page then loads the default as it now is, so the next press sends the current version.
function DefaultButton({ preset }: { preset: Preset }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const queryClient = useQueryClient();
  const latest = useQuery(routingDefaultQuery);
  const [note, setNote] = useState<"saved" | "reloaded" | null>(null);
  const save = useMutation({
    mutationFn: (presetId: string | null) =>
      unwrap(
        consoleApi.PUT("/api/console/v1/routing-default", {
          body: { preset_id: presetId, version: latest.data?.version ?? 0 },
        }),
      ),
    onSuccess: (saved) => {
      queryClient.setQueryData(routingDefaultQuery.queryKey, saved);
      setNote("saved");
    },
  });
  // After a refusal the page loads the default as it now is, which drops the refusal: the only way on is the current
  // version, and the button says what happened so the press can be repeated.
  const reload = useMutation({
    mutationFn: () => queryClient.fetchQuery({ ...routingDefaultQuery, staleTime: 0 }),
    onSuccess: () => {
      save.reset();
      setNote("reloaded");
    },
  });
  const isDefault = preset.id === newKeysPreset(latest.data);
  return (
    <div className="space-y-2">
      <p className="text-label text-t2">{t("routingPresets.defaults.hint")}</p>
      <Inherited read={latest.data} />
      <NewKeysWarning />
      <Button
        size="md"
        disabled={save.isPending}
        onClick={() =>
          save.mutate(isDefault ? null : preset.id, {
            onError: (error) => {
              if (error instanceof ApiError && error.code === "stale_revision") {
                reload.mutate();
              }
            },
          })
        }
      >
        {t(isDefault ? "routingPresets.defaults.clear" : "routingPresets.defaults.set")}
      </Button>
      {save.isError ? <FormError>{errors.message(save.error)}</FormError> : null}
      {reload.isError ? <FormError>{errors.message(reload.error)}</FormError> : null}
      {note ? (
        <p role="status">
          {t(note === "saved" ? "routingPresets.defaults.saved" : "routingPresets.defaults.reloaded")}
        </p>
      ) : null}
    </div>
  );
}

// Inherited says what the account's own default leaves to the site or the platform, naming the preset a key made now
// takes when the node names it. The names of the layers' presets are only known while the account sets none.
function Inherited({ read }: { read: Schemas["AccountRoutingDefault"] | undefined }) {
  const { t } = useTranslation();
  const newKeys = useNewKeys();
  if (!read || read.preset_id) {
    return null;
  }
  return (
    <p className="text-label text-t3">
      {newKeys.layer !== "workspace" && newKeys.name
        ? t("routingPresets.defaults.noneNamed", { name: newKeys.name })
        : t("routingPresets.defaults.none")}
    </p>
  );
}

// NewKeysWarning says when the account's default does not decide what a key made now takes: the default refuses new
// keys, or the default workspace, where the console makes keys, sets its own default, which comes before the account's.
// An older node says neither, and nothing is said.
function NewKeysWarning() {
  const { t } = useTranslation();
  const newKeys = useNewKeys();
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

// newKeysPreset is the preset a key made now takes: the node names it, or names none because the default refuses new
// keys; only an older node, which says neither, leaves the account's own default to stand for it.
function newKeysPreset(read: Schemas["AccountRoutingDefault"] | undefined) {
  return read?.new_keys_refused ? undefined : (read?.new_keys_preset_id ?? read?.preset_id);
}
