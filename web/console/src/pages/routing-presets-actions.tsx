import { ApiError, consoleApi, type Schemas, unwrap } from "@axiom/api";
import { can, RulesSummary, useErrors, useSession } from "@axiom/app";
import { Button, Dialog, FormError } from "@axiom/ui";
import { type UseQueryResult, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useNewKeys } from "../key-form";
import { DisablePresetDialog, PresetDialog, type PresetEdit } from "../preset-dialogs";
import { routingDefaultQuery } from "../queries";

type Preset = Schemas["RoutingPreset"];

// What the last write said: the account's default was set, cleared, or read again after another change raced with it.
const NOTES = {
  saved: "routingPresets.defaults.saved",
  cleared: "routingPresets.defaults.cleared",
  reloaded: "routingPresets.defaults.reloaded",
} as const;

// PresetActionsDialog is a preset row's ways on: open it to change it, copy it into one of the account's own, or disable
// it, and what a key made now takes. The board's table draws a row's name as a button and nothing else, so what it
// cannot draw lives here; only the account's own presets (and its workspaces') can be changed or disabled.
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
          {preset.keys === undefined ? null : (
            <p className="text-t3">{t("routingPresets.keysBound", { count: preset.keys })}</p>
          )}
        </div>
        <Defaults preset={preset} />
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

// Defaults says what a key made now takes, and lets a manager point the account's own default at the preset in hand or
// clear it. The account's default is the preset it names — not what new keys take, which the default workspace's own
// default comes before — so only the account's `preset_id` decides these buttons, and a workspace's preset cannot be the
// account's default at all. Clearing is its own control, so the account can go back to the site's or the platform's
// default even when the preset it named is not in the list.
export function Defaults({ preset }: { preset?: Preset }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const manage = can(useSession(), "api_keys.manage");
  const latest = useQuery(routingDefaultQuery);
  const [note, setNote] = useState<Note>(null);
  const { save, reload, write } = useDefaultWrite(latest, setNote);
  const read = latest.data;
  const isAccountDefault = read !== undefined && preset !== undefined && read.preset_id === preset.id;
  const settable = preset !== undefined && preset.scope !== "workspace" && !isAccountDefault;
  if (latest.isError) {
    return (
      <div className="space-y-2">
        <p className="text-label text-t2">{t("routingPresets.defaults.hint")}</p>
        <FormError>{errors.message(latest.error)}</FormError>
        <Button size="md" disabled={reload.isPending} onClick={() => reload.mutate("read")}>
          {t("routingPresets.defaults.reload")}
        </Button>
      </div>
    );
  }
  if (read === undefined) {
    return (
      <div className="space-y-2">
        <p className="text-label text-t2">{t("routingPresets.defaults.hint")}</p>
        <p className="text-label text-t3">{t("routingPresets.defaults.loading")}</p>
      </div>
    );
  }
  return (
    <div className="space-y-2">
      <p className="text-label text-t2">{t("routingPresets.defaults.hint")}</p>
      <Inherited read={read} />
      <NewKeysWarning />
      {manage ? (
        <DefaultWrite
          busy={save.isPending}
          hasDefault={read.preset_id !== undefined}
          isAccountDefault={isAccountDefault}
          preset={settable ? preset : undefined}
          onWrite={write}
        />
      ) : null}
      {save.isError ? <FormError>{errors.message(save.error)}</FormError> : null}
      {reload.isError ? <FormError>{errors.message(reload.error)}</FormError> : null}
      {note ? <p role="status">{t(NOTES[note])}</p> : null}
    </div>
  );
}

// DefaultWrite is what a manager can do to the account's default: point it at the preset in hand, or clear it — which is
// its own control, so the account can go back to the site's or the platform's default even when the preset it named is
// not in the list, and a workspace's preset, which cannot be the account's default, offers only the clear.
function DefaultWrite({
  busy,
  hasDefault,
  isAccountDefault,
  preset,
  onWrite,
}: {
  busy: boolean;
  hasDefault: boolean;
  isAccountDefault: boolean;
  preset: Preset | undefined;
  onWrite: (presetId: string | null) => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-wrap gap-2">
      {preset ? (
        <Button size="md" disabled={busy} onClick={() => onWrite(preset.id)}>
          {t("routingPresets.defaults.set")}
        </Button>
      ) : null}
      {hasDefault ? (
        <Button size="md" disabled={busy} onClick={() => onWrite(null)}>
          {t(isAccountDefault ? "routingPresets.defaults.clear" : "routingPresets.defaults.clearAccount")}
        </Button>
      ) : null}
    </div>
  );
}

// What the last write said: the account's default was set, cleared, or read again after another change raced with it.
type Note = "saved" | "cleared" | "reloaded" | null;

// useDefaultWrite points the account's default at a preset, or clears it. The version the page read goes with the
// change; a change made elsewhere meanwhile is refused (stale_revision) instead of overwritten, and the page then loads
// the default as it now is, so the next press sends the current version.
function useDefaultWrite(latest: UseQueryResult<Schemas["AccountRoutingDefault"]>, setNote: (note: Note) => void) {
  const queryClient = useQueryClient();
  const save = useMutation({
    mutationFn: (presetId: string | null) =>
      unwrap(
        consoleApi.PUT("/api/console/v1/routing-default", {
          // The read is what the write is based on: no version, no write.
          body: { preset_id: presetId, version: latest.data?.version as number },
        }),
      ),
    onMutate: () => setNote(null),
    onSuccess: (saved, presetId) => {
      queryClient.setQueryData(routingDefaultQuery.queryKey, saved);
      setNote(presetId === null ? "cleared" : "saved");
    },
  });
  // Reading the default again says two different things: after a refusal it is the way on, and the page says so; from the
  // button a failed first read left, it is only the read that now works, and the lines below say what the default is.
  const reload = useMutation({
    mutationFn: (_kind: "conflict" | "read") => queryClient.fetchQuery({ ...routingDefaultQuery, staleTime: 0 }),
    onMutate: () => setNote(null),
    onSuccess: (_read, kind) => {
      save.reset();
      setNote(kind === "conflict" ? "reloaded" : null);
    },
  });
  const write = (presetId: string | null) =>
    save.mutate(presetId, {
      onError: (error) => {
        if (error instanceof ApiError && error.code === "stale_revision") {
          reload.mutate("conflict");
        }
      },
    });
  return { save, reload, write };
}

// Inherited says what the account's own default leaves to the site or the platform, naming the preset a key made now
// takes when the node names it. The names of the layers' presets are only known while the account sets none.
function Inherited({ read }: { read: Schemas["AccountRoutingDefault"] }) {
  const { t } = useTranslation();
  const newKeys = useNewKeys();
  if (read.preset_id) {
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
// An older node says neither, and nothing is said. It is read by everyone who may see presets: a member who may not
// change them still needs to know that a key made without a preset is refused.
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
