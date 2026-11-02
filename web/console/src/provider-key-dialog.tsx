import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { accountKey, readForm, useErrors } from "@axiom/app";
import { Button, CheckField, Dialog, FormError, SelectField, SubmitButton, TextField } from "@axiom/ui";
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
  return (
    <Dialog title={t("providerKeys.add")} onClose={onClose}>
      <form
        className="space-y-4"
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
        {create.isError ? <FormError>{errors.message(create.error)}</FormError> : null}
        {error ? <FormError>{error}</FormError> : null}
        <TextField label={t("providerKeys.name")} name="name" required maxLength={200} autoFocus />
        <SelectField label={t("providerKeys.provider")} name="provider" defaultValue="openai">
          <option value="openai">{t("providerKeys.openai")}</option>
        </SelectField>
        <TextField
          label={t("providerKeys.key")}
          name="key"
          type="password"
          required
          autoComplete="off"
          hint={t("providerKeys.keyHint")}
        />
        <CheckField name="fallback" label={t("providerKeys.fallback")} defaultChecked />
        <div className="flex justify-end gap-2">
          <Button type="button" size="md" onClick={onClose}>
            {t("cancel", { ns: "common" })}
          </Button>
          <SubmitButton pending={create.isPending}>{t("providerKeys.save")}</SubmitButton>
        </div>
      </form>
    </Dialog>
  );
}
