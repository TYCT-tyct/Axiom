import { ApiError, consoleApi, type Schemas, unwrap, unwrapEmpty } from "@axiom/api";
import {
  accountKey,
  draftOf,
  type Invalid,
  RulesEditor,
  rulesOf,
  useClassOptions,
  useErrors,
  useRulesCheck,
} from "@axiom/app";
import { Button, Cluster, Dialog, FormError, Icon, TextField, useFormat } from "@axiom/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ConfirmDialog } from "./confirm-dialog";
import { routingDefaultQuery, routingPresetsQuery, siteQuery, sourceClassesQuery } from "./queries";
import { Actions } from "./workspace-dialogs";

type Preset = Schemas["RoutingPreset"];
export type PresetEdit = { kind: "create" } | { kind: "copy"; preset: Preset } | { kind: "edit"; preset: Preset };

const slugPattern = /^[a-z0-9][a-z0-9-]{0,63}$/;

// A preset or its default changed: the presets page, the default and the key list, which shows each key's preset by
// name, ask again.
function useRefreshPresets() {
  const queryClient = useQueryClient();
  return () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: [accountKey, "routing-presets"] }),
      queryClient.invalidateQueries({ queryKey: [accountKey, "keys"] }),
    ]);
}

// A change made elsewhere moved the revision on. Reading the list again gives the row a dialog was opened from the
// revision a second attempt names, and the dialog says to reopen only once that read has it: while it reads it says so,
// and if it fails it offers the read again.
type Stale = { state: "idle" | "reading" | "read" | "failed"; read: () => void; reset: () => void };
function useStaleRefresh(): Stale {
  const queryClient = useQueryClient();
  const [state, setState] = useState<Stale["state"]>("idle");
  const read = () => {
    setState("reading");
    // The list the dialog was opened from, read as the node now has it: it is what the next attempt takes its revision
    // from, so a read that fails is said rather than passed over.
    queryClient.fetchQuery({ ...routingPresetsQuery, staleTime: 0 }).then(
      () => setState("read"),
      () => setState("failed"),
    );
  };
  return { state, read, reset: () => setState("idle") };
}

function isStale(error: unknown) {
  return error instanceof ApiError && error.code === "stale_revision";
}

// StaleNote says what the dialog knows of that read: that it is reading, then what to do — or that it could not read,
// with the read again.
function StaleNote({ stale }: { stale: Stale }) {
  const { t } = useTranslation();
  if (stale.state === "reading") {
    return <p>{t("routingPresets.form.staleReading")}</p>;
  }
  if (stale.state === "read") {
    return <p>{t("routingPresets.form.staleHint")}</p>;
  }
  if (stale.state === "failed") {
    return (
      <Cluster gap={8}>
        <p>{t("routingPresets.form.staleFailed")}</p>
        <Button size="sm" onClick={stale.read}>
          {t("routingPresets.form.retry")}
        </Button>
      </Cluster>
    );
  }
  return null;
}

// SaveError says why a save failed. The format's checks name the rule they refuse (an override that widens, a price with
// too many decimals) in the server's words, which a translated sentence could not; a stale revision says what to do.
function SaveError({ error, stale }: { error: unknown; stale: Stale }) {
  const { t } = useTranslation();
  const errors = useErrors();
  if (!error) {
    return null;
  }
  const detail = error instanceof ApiError ? error.fields.find((field) => field.pointer === "/rules") : undefined;
  return (
    <FormError>
      <p>{errors.message(error)}</p>
      {detail ? (
        <p className="text-label">{t("routingPresets.form.rulesRefused", { detail: detail.message })}</p>
      ) : null}
      {isStale(error) ? <StaleNote stale={stale} /> : null}
    </FormError>
  );
}

function save(edit: PresetEdit, body: { slug: string; name: string; rules: Schemas["RoutingRules"] }) {
  if (edit.kind === "edit") {
    return unwrap(
      consoleApi.PUT("/api/console/v1/routing-presets/{preset_id}", {
        params: { path: { preset_id: edit.preset.id } },
        body: { name: body.name, rules: body.rules, revision: edit.preset.revision },
      }),
    );
  }
  return unwrap(consoleApi.POST("/api/console/v1/routing-presets", { body }));
}

type Missing = "slug" | "name" | null;

// checkRules asks which rules keep no endpoint the account's keys can use: the editor marks them as the rules change,
// and a save is refused while one does.
function checkRules(rules: Schemas["RoutingRules"]) {
  return unwrap(consoleApi.POST("/api/console/v1/routing-presets/checks", { body: { rules } }));
}

// A refusal may sit far down a long dialog, away from the button pressed: once it renders, it is scrolled into view.
function showRefusal(form: HTMLFormElement) {
  requestAnimationFrame(() =>
    form.querySelector("[aria-invalid=true], [data-invalid]")?.scrollIntoView({ block: "center" }),
  );
}

// What the rules editor offers: prices in the site's currency, and the source classes the node routes by.
function useEditorChoices() {
  const currency = useQuery(siteQuery).data?.currency ?? "";
  const classes = useClassOptions(useQuery({ ...sourceClassesQuery, select: (view) => view.data }));
  return { currency, classes };
}

// PresetDialog makes a preset, makes one from a copy of another's rules, or changes one of the account's. An edit is
// fixed to the revision it opened with: a change made elsewhere meanwhile is refused, not overwritten.
export function PresetDialog({ edit, onClose }: { edit: PresetEdit; onClose: () => void }) {
  const { t } = useTranslation();
  const refresh = useRefreshPresets();
  const stale = useStaleRefresh();
  const choices = useEditorChoices();
  const source = edit.kind === "create" ? undefined : edit.preset;
  const [slug, setSlug] = useState(edit.kind === "copy" ? edit.preset.slug : "");
  const [name, setName] = useState(source?.name ?? "");
  const [draft, setDraft] = useState(() => draftOf(source?.rules, edit.kind === "copy"));
  const [invalid, setInvalid] = useState<Invalid | null>(null);
  const [missing, setMissing] = useState<Missing>(null);
  const unmatched = useRulesCheck(draft, checkRules, []);
  const mutation = useMutation({
    mutationFn: (body: Parameters<typeof save>[1]) => save(edit, body),
    onSuccess: () => refresh().then(onClose),
    onMutate: stale.reset,
    onError: (error) => isStale(error) && stale.read(),
  });
  const submit = (form: HTMLFormElement) => {
    const written = rulesOf(draft);
    const blank: Missing = edit.kind !== "edit" && !slugPattern.test(slug) ? "slug" : name.trim() ? null : "name";
    setInvalid("invalid" in written ? written.invalid : null);
    setMissing(blank);
    if ("rules" in written && !blank && !unmatched && !mutation.isPending) {
      mutation.mutate({ slug, name: name.trim(), rules: written.rules });
      return;
    }
    showRefusal(form);
  };
  return (
    <Dialog title={t(`routingPresets.${edit.kind}Title`)} onClose={onClose}>
      <form
        noValidate
        className="space-y-4"
        onSubmit={(event) => {
          event.preventDefault();
          submit(event.currentTarget);
        }}
      >
        <SaveError error={mutation.error} stale={stale} />
        {edit.kind === "copy" ? <p className="text-label text-t3">{t("routingPresets.form.copyNote")}</p> : null}
        <Names
          edit={edit}
          slug={slug}
          name={name}
          missing={missing}
          error={mutation.error}
          onSlug={setSlug}
          onName={setName}
        />
        <RulesEditor draft={draft} invalid={invalid ?? unmatched} {...choices} onChange={setDraft} />
        <Actions
          pending={mutation.isPending}
          submit={edit.kind === "edit" ? t("routingPresets.form.save") : t("routingPresets.form.create")}
          onCancel={onClose}
        />
      </form>
    </Dialog>
  );
}

function Names({
  edit,
  slug,
  name,
  missing,
  error,
  onSlug,
  onName,
}: {
  edit: PresetEdit;
  slug: string;
  name: string;
  missing: Missing;
  error: unknown;
  onSlug: (slug: string) => void;
  onName: (name: string) => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <TextField
        label={t("routingPresets.form.name")}
        value={name}
        maxLength={100}
        autoComplete="off"
        error={missing === "name" ? t("routingPresets.form.nameMissing") : errors.field(error, "name")}
        onChange={(event) => onName(event.currentTarget.value)}
      />
      {edit.kind === "edit" ? null : (
        <TextField
          label={t("routingPresets.form.slug")}
          hint={t("routingPresets.form.slugHint")}
          value={slug}
          maxLength={64}
          autoComplete="off"
          spellCheck={false}
          error={missing === "slug" ? t("routingPresets.form.slugInvalid") : errors.field(error, "slug")}
          onChange={(event) => onSlug(event.currentTarget.value)}
        />
      )}
    </div>
  );
}

// DisablePresetDialog disables one of the account's presets at the revision the list showed. Before that it says how
// many of the account's keys are bound to it and whether it is the account's default; a default that still points at
// it refuses (preset_in_use), and the message says to change the default first.
export function DisablePresetDialog({ preset, onClose }: { preset: Preset; onClose: () => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  const isDefault = useQuery(routingDefaultQuery).data?.preset_id === preset.id;
  const refresh = useRefreshPresets();
  const stale = useStaleRefresh();
  const disable = useMutation({
    mutationFn: () =>
      unwrapEmpty(
        consoleApi.DELETE("/api/console/v1/routing-presets/{preset_id}", {
          params: { path: { preset_id: preset.id }, query: { revision: preset.revision } },
        }),
      ),
    onSuccess: async () => {
      await refresh();
      onClose();
    },
    onMutate: stale.reset,
    onError: (error) => isStale(error) && stale.read(),
  });
  return (
    <ConfirmDialog
      detail={<StaleNote stale={stale} />}
      title={t("routingPresets.disable.title")}
      body={
        <>
          {isDefault ? (
            <p className="flex gap-2 rounded-btn bg-warn-soft px-3 py-2 text-warn">
              <Icon name="alert" className="mt-0.5 shrink-0" />
              {t("routingPresets.disable.isDefault")}
            </p>
          ) : null}
          {/* A node that does not count the keys gives no number, and then none is said. */}
          {preset.keys === undefined ? null : (
            <p>
              {preset.keys === 0
                ? t("routingPresets.disable.noKeys")
                : t("routingPresets.disable.keys", { count: preset.keys, keys: format.number(preset.keys) })}
            </p>
          )}
          <p>{t("routingPresets.disable.body", { name: preset.name })}</p>
        </>
      }
      confirm={t("routingPresets.disable.confirm")}
      action={disable}
      onClose={onClose}
    />
  );
}
