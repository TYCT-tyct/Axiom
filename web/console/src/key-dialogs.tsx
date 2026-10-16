import { consoleApi, type Schemas, unwrap, unwrapEmpty } from "@axiom/api";
import { Button, CopyButton, Dialog, FormError, SubmitButton } from "@axiom/ui";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useErrors } from "./errors";
import { KeyForm } from "./key-form";
import { accountKey } from "./session";

type Key = Schemas["Key"];

// The key list and the overview read the same cache entry; a change to a key makes both ask again.
function useRefreshKeys() {
  const queryClient = useQueryClient();
  return () => queryClient.invalidateQueries({ queryKey: [accountKey, "keys"] });
}

export function CreateKeyDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (key: Schemas["CreatedKey"]) => void;
}) {
  const { t } = useTranslation();
  const refresh = useRefreshKeys();
  const create = useMutation({
    mutationFn: (body: Schemas["KeyCreate"]) => unwrap(consoleApi.POST("/api/console/v1/keys", { body })),
    onSuccess: async (created) => {
      await refresh();
      onCreated(created);
    },
  });
  return (
    <Dialog title={t("keys.create")} onClose={onClose}>
      <KeyForm
        pending={create.isPending}
        error={create.error}
        onCancel={onClose}
        onSubmit={({ disabled: _disabled, ...body }) => create.mutate(body)}
      />
    </Dialog>
  );
}

export function EditKeyDialog({ apiKey, onClose }: { apiKey: Key; onClose: () => void }) {
  const { t } = useTranslation();
  const refresh = useRefreshKeys();
  const update = useMutation({
    mutationFn: (body: Schemas["KeySettings"]) =>
      unwrap(consoleApi.PUT("/api/console/v1/keys/{key_id}", { params: { path: { key_id: apiKey.id } }, body })),
    onSuccess: async () => {
      await refresh();
      onClose();
    },
  });
  return (
    <Dialog title={t("keys.edit")} onClose={onClose}>
      <KeyForm
        apiKey={apiKey}
        pending={update.isPending}
        error={update.error}
        onCancel={onClose}
        onSubmit={({ expires_at: _expires, ...body }) => update.mutate(body)}
      />
    </Dialog>
  );
}

export function DeleteKeyDialog({ apiKey, onClose }: { apiKey: Key; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const refresh = useRefreshKeys();
  const remove = useMutation({
    mutationFn: () =>
      unwrapEmpty(consoleApi.DELETE("/api/console/v1/keys/{key_id}", { params: { path: { key_id: apiKey.id } } })),
    onSuccess: async () => {
      await refresh();
      onClose();
    },
  });
  return (
    <Dialog title={t("keys.delete.title")} onClose={onClose}>
      <div className="space-y-4">
        {remove.isError ? <FormError>{errors.message(remove.error)}</FormError> : null}
        <p className="text-t2 [overflow-wrap:anywhere]">{t("keys.delete.body", { name: apiKey.name })}</p>
        <div className="flex justify-end gap-2">
          <Button size="md" onClick={onClose}>
            {t("cancel")}
          </Button>
          <SubmitButton
            pending={remove.isPending}
            variant="danger"
            onClick={(event) => {
              event.preventDefault();
              remove.mutate();
            }}
          >
            {t("keys.delete.confirm")}
          </SubmitButton>
        </div>
      </div>
    </Dialog>
  );
}

// The key's text exists only in this answer. The dialog does not close on Escape or a click outside, so it cannot be
// lost by accident; the user closes it once the key is copied.
export function SecretDialog({ created, onClose }: { created: Schemas["CreatedKey"]; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <Dialog title={t("keys.secret.title")} onClose={onClose} dismissible={false}>
      <div className="space-y-4">
        <p className="text-t2">{t("keys.secret.body", { name: created.key.name })}</p>
        <code className="block rounded-btn border border-b2 bg-s2 p-3 font-mono text-ui break-all text-t1 select-all">
          {created.secret}
        </code>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-3">
            <CopyButton text={created.secret} />
          </div>
          <Button variant="primary" size="md" onClick={onClose}>
            {t("keys.secret.done")}
          </Button>
        </div>
      </div>
    </Dialog>
  );
}
