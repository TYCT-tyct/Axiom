import { ApiError, consoleApi, type Schemas, unwrap, unwrapEmpty } from "@axiom/api";
import { accountKey } from "@axiom/app";
import Editor from "@axiom/boards/AxApp/regions/RequestPresetEditor";
import Input from "@axiom/boards/AxApp/regions/RequestPresetInput";
import { type QueryKey, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useId, useRef, useState } from "react";
import type { zh } from "../locales/request-presets";

type Copy = typeof zh;
const path = "/api/console/v1/presets/{slug}";
type Change = { kind: "save"; body: Schemas["PresetRequest"] } | { kind: "delete" };

export function PresetDialog({
  slug,
  opener,
  manage,
  listKey,
  copy: text,
  onClose,
}: {
  slug: string;
  opener: HTMLElement;
  manage: boolean;
  listKey: QueryKey;
  copy: Copy;
  onClose: () => void;
}) {
  const params = { path: { slug } };
  const detail = useQuery({
    queryKey: [accountKey, "request-preset", slug],
    enabled: !!slug,
    queryFn: ({ signal }) => unwrap(consoleApi.GET(path, { params, signal })),
    // A draft has one base for its lifetime. Reopening reads the current config.
    staleTime: Infinity,
    gcTime: 0,
  });
  const mutation = usePresetWrite(slug, listKey, onClose);
  const busy = mutation.isPending;
  const ref = useModal(opener, busy, onClose);
  const titleId = useId();
  const preset = detail.data;
  const ready = !slug || !!preset;
  const edit = manage && ready;
  const draft = usePresetDraft(preset, edit, text, mutation);
  const title = !slug ? text.create : manage ? text.edit : text.detail;
  const labels: Record<string, string> = {
    m3fd47edce4: text.close,
    md6d8ddd71a: text.systemHint,
    m2cd0f3be87: text.cancel,
  };

  return (
    <form onSubmit={draft.submit}>
      <Editor
        t={(key) => labels[key] ?? ""}
        text={{
          title,
          save: busy ? text.saving : text.save,
          remove: draft.confirm ? text.confirm : text.remove,
          promptLabel: text.system,
        }}
        values={{
          titleId,
          dialogRef: (element) => {
            ref.current = element as HTMLDialogElement | null;
          },
          busy,
          readOnly: !edit,
          hideDelete: !manage || !preset,
          hideFields: !ready,
          close: onClose,
          deletePreset: draft.remove,
          system: asText(preset?.config?.system),
        }}
        inputs={ready && <PresetInputs preset={preset} text={text} titleId={titleId} busy={busy} readOnly={!edit} />}
        note={
          ready ? (
            <EditorNote notice={draft.notice} slug={slug} hint={text.referenceHint} />
          ) : (
            <ReadNotice error={detail.error} text={text} retry={() => void detail.refetch()} />
          )
        }
      />
    </form>
  );
}

function usePresetDraft(
  preset: Schemas["Preset"] | undefined,
  edit: boolean,
  text: Copy,
  mutation: ReturnType<typeof usePresetWrite>,
) {
  const [confirm, setConfirm] = useState(false);
  const [invalid, setInvalid] = useState<string>();
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!edit || mutation.isPending) return;
    const submitted = requestBody(new FormData(event.currentTarget), preset, text);
    if (typeof submitted === "string") {
      setInvalid(submitted);
      return;
    }
    setInvalid(undefined);
    setConfirm(false);
    mutation.mutate({ kind: "save", body: submitted });
  };
  const remove = () => {
    setInvalid(undefined);
    if (confirm) {
      mutation.mutate({ kind: "delete" });
    } else {
      mutation.reset();
      setConfirm(true);
    }
  };
  const notice = invalid || (mutation.isError ? mutationMessage(mutation.variables, mutation.error, text) : undefined);
  return { submit, remove, confirm, notice };
}

function useModal(opener: HTMLElement, busy: boolean, onClose: () => void) {
  const ref = useRef<HTMLDialogElement | null>(null);
  useEffect(() => {
    const dialog = ref.current;
    dialog?.showModal();
    return () => {
      if (dialog?.open) dialog.close();
      const target = opener.isConnected ? opener : document.querySelector<HTMLElement>(".p-presets h1");
      target?.focus();
    };
  }, [opener]);
  useEffect(() => {
    const dialog = ref.current;
    const cancel = (event: Event) => {
      event.preventDefault();
      if (!busy) onClose();
    };
    dialog?.addEventListener("cancel", cancel);
    return () => dialog?.removeEventListener("cancel", cancel);
  }, [busy, onClose]);
  return ref;
}

async function writePreset(slug: string, change: Change) {
  const params = { path: { slug } };
  if (change.kind === "delete") {
    await unwrapEmpty(consoleApi.DELETE(path, { params }));
    return undefined;
  }
  const { name, config } = change.body;
  return slug
    ? unwrap(consoleApi.PUT(path, { params, body: { name, config } }))
    : unwrap(consoleApi.POST("/api/console/v1/presets", { body: change.body }));
}

function usePresetWrite(slug: string, listKey: QueryKey, onClose: () => void) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (change: Change) => writePreset(slug, change),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: listKey });
      onClose();
    },
  });
}

function EditorNote({ notice, slug, hint }: { notice?: string; slug: string; hint: string }) {
  if (notice) return <span role="alert">{notice}</span>;
  return slug ? <code>{`@preset/${slug}`}</code> : hint;
}

function ReadNotice({ error, text, retry }: { error: unknown; text: Copy; retry: () => void }) {
  if (error instanceof ApiError && error.status === 403) return <span role="status">{text.forbidden}</span>;
  if (!error) return <span role="status">{text.loading}</span>;
  return (
    <span role="alert">
      {text.detailFailed}{" "}
      <button type="button" onClick={retry}>
        {text.retry}
      </button>
    </span>
  );
}

function mutationMessage(change: Change | undefined, error: unknown, text: Copy) {
  const hint = change?.kind === "delete" ? text.deleteFailed : text.saveFailed;
  if (error instanceof ApiError && error.status === 403) return `${hint} ${text.forbiddenHint}`;
  return error instanceof ApiError && error.requestId ? `${hint} (${error.requestId})` : hint;
}

function PresetInputs({
  preset,
  text,
  titleId,
  busy,
  readOnly,
}: {
  preset?: Schemas["Preset"];
  text: Copy;
  titleId: string;
  busy: boolean;
  readOnly: boolean;
}) {
  const c = preset?.config ?? {};
  const fields = [
    { name: "name", label: text.name, value: preset?.name ?? "" },
    { name: "slug", label: text.slug, value: "" },
    { name: "model", label: text.model, value: asText(c.model) },
    { name: "temperature", label: text.temperature, value: asText(c.temperature) },
    { name: "max", label: text.tokens, value: asText(c[tokenField(c)]) },
    { name: "models", label: text.fallback, value: asText(c.models) },
  ];
  return fields
    .filter(({ name }) => !preset || name !== "slug")
    .map(({ name, label, value }) => (
      <Input
        key={name}
        t={() => ""}
        text={{ label }}
        values={{ id: `${titleId}-${name}`, name, initial: value, readOnly, busy }}
      />
    ));
}

function asText(value: unknown) {
  if (typeof value === "string") return value;
  return value === undefined ? "" : JSON.stringify(value);
}

function tokenField(config: Schemas["PresetConfig"]) {
  if (config.max_tokens !== undefined) return "max_tokens";
  if (config.max_completion_tokens !== undefined) return "max_completion_tokens";
  return config.max_output_tokens === undefined ? "max_tokens" : "max_output_tokens";
}

function fallbackList(value: string): string[] | undefined {
  try {
    const models: unknown = JSON.parse(value);
    if (Array.isArray(models) && models.length <= 8 && models.every((name) => typeof name === "string")) return models;
  } catch {
    // The same field help covers malformed JSON and values outside the contract.
  }
  return undefined;
}

function requestBody(input: FormData, preset: Schemas["Preset"] | undefined, text: Copy) {
  const read = (key: string) => String(input.get(key) ?? "");
  const name = read("name");
  const slug = preset?.slug ?? read("slug");
  if (!name || [...name].length > 100) return text.invalidName;
  if (!/^[A-Za-z0-9_-]{1,100}$/.test(slug)) return text.invalidSlug;
  const config = { ...preset?.config };
  // PUT replaces the entire config. Keep fields outside the editor and members whose input was left as it was.
  const fields = {
    model: "model",
    system: "system",
    temperature: "temperature",
    max: tokenField(config),
    models: "models",
  };
  for (const [field, key] of Object.entries(fields)) {
    const value = read(field);
    if (value === asText(config[key])) continue;
    const error = setConfigField(config, key, value, text);
    if (error) return error;
  }
  return { slug, name, config };
}

function setConfigField(config: Schemas["PresetConfig"], key: string, value: string, text: Copy) {
  if (!value) {
    delete config[key];
    return;
  }
  if (key === "models") {
    const models = fallbackList(value);
    if (!models) return text.invalidFallback;
    config[key] = models;
    return;
  }
  if (key === "temperature" || key.startsWith("max_")) {
    const number = Number(value);
    if (!value.trim() || !Number.isFinite(number)) return text.invalidNumber;
    config[key] = number;
    return;
  }
  config[key] = value;
}
