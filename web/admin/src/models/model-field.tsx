import { TextField } from "@axiom/ui";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useDeferredValue, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { modelLookupQuery } from "../queries";

// ModelField chooses a model by typing its name: the browser's own suggestion list offers the models whose slug starts
// with what is typed, and the form carries the ID of the one whose slug was typed in full (model_id), or nothing.
export function ModelField({ label, error, currentId }: { label: string; error?: string; currentId?: string }) {
  const { t } = useTranslation();
  const list = useId();
  const [typed, setTyped] = useState("");
  const prefix = useDeferredValue(typed.trim());
  const found = useQuery({ ...modelLookupQuery(prefix), placeholderData: keepPreviousData });
  const models = found.data?.data ?? [];
  const chosen = models.find((model) => model.slug === typed.trim());
  const kept = typed.trim() === "" ? currentId : undefined;
  let hint = t("models.field.hint");
  if (chosen) {
    hint = t("models.field.chosen", { status: t(`models.state.${chosen.status}`) });
  } else if (kept) {
    hint = t("models.field.kept", { id: kept });
  }
  return (
    <>
      <TextField
        label={label}
        hint={hint}
        name="model"
        list={list}
        value={typed}
        onChange={(event) => setTyped(event.target.value)}
        autoComplete="off"
        spellCheck={false}
        maxLength={182}
        error={error}
      />
      <datalist id={list}>
        {models.map((model) => (
          <option key={model.id} value={model.slug} />
        ))}
      </datalist>
      <input type="hidden" name="model_id" value={chosen?.id ?? kept ?? ""} />
    </>
  );
}
