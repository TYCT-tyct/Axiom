import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { Button, Cluster, Dialog, SettingRow, Stack, useFormat } from "@axiom/ui";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { KeyForm, useNewKeys, usePresetName } from "../key-form";
import { maskedKey } from "../parts";
import { useWorkspaces } from "../workspaces";

type Key = Schemas["Key"];

// A key's settings, and the two things that end its use: switching it off, which can be undone, and deleting it, which
// cannot. The board draws a key's detail as a drawer whose editors it opens in JavaScript, which the region converter
// does not read (#747 C1), so the page keeps the console's dialog; what the drawer would say that the page can answer
// from the API — who made the key, when, when it expires, when it was last used — is read-only here.
export function KeyDetailDialog({
  apiKey,
  onClose,
  onDelete,
  onSaved,
}: {
  apiKey: Key;
  onClose: () => void;
  onDelete: (key: Key) => void;
  // Every save, with the key as it was before it: the page keeps its undo offer only for the save that flipped the
  // switch, and drops it for any other.
  onSaved: (key: Key, before: Key) => void;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const update = useMutation({
    mutationFn: (body: Schemas["KeySettings"]) =>
      unwrap(consoleApi.PUT("/api/console/v1/keys/{key_id}", { params: { path: { key_id: apiKey.id } }, body })),
    onSuccess: async (changed) => {
      onSaved(changed, apiKey);
      await queryClient.invalidateQueries({ queryKey: [accountKey, "keys"] });
      onClose();
    },
  });
  return (
    <Dialog title={t("keys.edit")} onClose={onClose}>
      <Stack gap={16}>
        <Facts apiKey={apiKey} />
        <KeyForm
          apiKey={apiKey}
          pending={update.isPending}
          error={update.error}
          onCancel={onClose}
          // The contract replaces every setting it is given; the preset stays as it is unless another is chosen.
          onSubmit={({ expires_at: _expires, workspace_id: _workspace, preset_id, ...body }) =>
            update.mutate(preset_id && preset_id !== apiKey.preset_id ? { ...body, preset_id } : body)
          }
        />
        <Cluster justify="end">
          <Button size="md" variant="danger" onClick={() => onDelete(apiKey)}>
            {t("keys.deleteNamed", { name: apiKey.name })}
          </Button>
        </Cluster>
      </Stack>
    </Dialog>
  );
}

// Facts is what the page can read about a key without changing it, including the preset it is routed by and whether it
// is what a key made now would take (L3 in /home/ai/axiom-ops/design/console-gap.md): a default that moves on does not
// move the keys already made. A management key calls no model, so it has no preset.
function Facts({ apiKey }: { apiKey: Key }) {
  const { t } = useTranslation();
  const format = useFormat();
  const presetName = usePresetName();
  const newKeys = useNewKeys(useWorkspaces().scopeOf(apiKey.workspace_id));
  const moved = apiKey.preset_id !== undefined && newKeys.id !== undefined && newKeys.id !== apiKey.preset_id;
  const presetNote = newKeys.name
    ? t("routingPresets.key.notDefaultNamed", { name: newKeys.name })
    : t("routingPresets.key.notDefault");
  const facts: { name: string; value: ReactNode }[] = [
    { name: t("keys.board.text"), value: maskedKey(apiKey) },
    ...(apiKey.kind === "management"
      ? []
      : [
          {
            name: t("routingPresets.key.field"),
            value: moved ? `${presetName(apiKey)} · ${presetNote}` : presetName(apiKey),
          },
        ]),
    { name: t("keys.board.creator"), value: apiKey.created_by?.display_name ?? "—" },
    { name: t("keys.board.created"), value: format.dateTime(new Date(apiKey.created_at)) },
    {
      name: t("keys.board.expires"),
      value: apiKey.expires_at ? format.dateTime(new Date(apiKey.expires_at)) : t("keys.neverExpires"),
    },
    {
      name: t("keys.board.lastUsed"),
      value: apiKey.last_used_at ? format.dateTime(new Date(apiKey.last_used_at)) : t("keys.board.neverUsed"),
    },
  ];
  return (
    <div>
      {facts.map(({ name, value }) => (
        <SettingRow key={name} title={name}>
          <span>{value}</span>
        </SettingRow>
      ))}
    </div>
  );
}
