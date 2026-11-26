import { ApiError, consoleApi, type Schemas, unwrap } from "@axiom/api";
import { accountKey, readForm, useErrors } from "@axiom/app";
import { Button, CheckField, Cluster, Dialog, FormError, SelectField, Stack, SubmitButton, TextField } from "@axiom/ui";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";

// AddProviderKeyDialog saves one key. The key is a password field and is never read back: the list shows only its last
// four characters after saving.
export function AddProviderKeyDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const queryClient = useQueryClient();
  const [error, setError] = useState<string | null>(null);
  const create = useMutation({
    mutationFn: (body: Schemas["ProviderKeyCreate"]) =>
      unwrap(consoleApi.POST("/api/console/v1/provider-keys", { body })),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: [accountKey, "provider-keys"] });
      onClose();
    },
  });
  // A refused key names the field it is about and why, such as a provider this node cannot call; the reason goes beside
  // the field it names, as the server wrote it.
  const reason = (name: string) =>
    create.error instanceof ApiError ? create.error.fields.find(({ pointer }) => pointer === `/${name}`)?.message : undefined;
  return (
    <Dialog title={t("providerKeys.add")} onClose={onClose}>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          const values = readForm(event.currentTarget);
          const name = (values.name ?? "").trim();
          const key = (values.key ?? "").trim();
          if (!name || !key) {
            setError(t("providerKeys.required"));
            return;
          }
          setError(null);
          create.mutate({ name, key, provider: "openai", fallback: values.fallback === "on" });
        }}
      >
        <Stack>
          {create.isError ? <FormError>{errors.message(create.error)}</FormError> : null}
          {error ? <FormError>{error}</FormError> : null}
          <TextField
            label={t("providerKeys.name")}
            name="name"
            required
            maxLength={200}
            autoFocus
            error={reason("name")}
          />
          <SelectField label={t("providerKeys.provider")} name="provider" defaultValue="openai" error={reason("provider")}>
            <option value="openai">{t("providerKeys.openai")}</option>
          </SelectField>
          <TextField
            label={t("providerKeys.key")}
            name="key"
            type="password"
            required
            autoComplete="off"
            hint={t("providerKeys.keyHint")}
            error={reason("key")}
          />
          <CheckField name="fallback" label={t("providerKeys.fallback")} defaultChecked />
          <Cluster justify="end">
            <Button type="button" size="md" onClick={onClose}>
              {t("cancel", { ns: "common" })}
            </Button>
            <SubmitButton pending={create.isPending}>{t("providerKeys.save")}</SubmitButton>
          </Cluster>
        </Stack>
      </form>
    </Dialog>
  );
}
