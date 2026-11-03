import { type AdminSchemas, ApiError, adminApi, unwrapChange, unwrapEmptyChange } from "@axiom/api";
import { Button, DataState, EmptyState, PageHeader, Panel, TextAreaField, TextField, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, isProblem, type Problem, useSnapshot } from "../change";
import { Code, PageBody, State } from "../parts";
import { sourceClassesQuery } from "../queries";
import { ConditionRows, type Rules, readRules } from "../source-class-rules";

type Class = AdminSchemas["SourceClass"];

// EditorInput is what both the create and the edit dialog read from their form. The slug is absent when editing.
type EditorInput = {
  slug?: string;
  name: string;
  name_zh: string;
  description: string;
  position?: number;
  rules?: Rules;
};

// SourceClassesPage lists the classes in recognition order. The fallback is still a class like the others; it just has
// no conditions and is chosen when nothing matches.
export default function SourceClassesPage() {
  const { t } = useTranslation();
  const query = useQuery(sourceClassesQuery);
  // A node of the release before has no such operation. DataState would call that a missing page; say what it is.
  if (query.isError && query.error instanceof ApiError && query.error.status === 404) {
    return (
      <>
        <PageHeader title={t("sourceClasses.title")} description={t("sourceClasses.description")} />
        <PageBody>
          <Panel title={t("sourceClasses.unavailableTitle")}>
            <p className="px-4 py-3 text-ui text-t2">{t("sourceClasses.unavailableBody")}</p>
          </Panel>
        </PageBody>
      </>
    );
  }
  return (
    <>
      <PageHeader title={t("sourceClasses.title")} description={t("sourceClasses.description")} />
      <PageBody>
        <DataState query={query}>{(view) => <Classes classes={view.classes} />}</DataState>
      </PageBody>
    </>
  );
}

// Editing is the open editor: a new class, or an existing one by slug. Every slug is a valid name for a class, "new"
// included, so creating is its own kind rather than a reserved slug.
type Editing = { kind: "create" } | { kind: "edit"; slug: string } | null;

// Classes is the list and the two dialogs it opens. The dialogs are mounted only while open, so each starts from the
// class as it was read and a stale revision is refused rather than silently applied.
export function Classes({ classes }: { classes: Class[] }) {
  const { t, i18n } = useTranslation();
  const chinese = i18n.language.startsWith("zh");
  const [editing, setEditing] = useState<Editing>(null);
  const [retiring, setRetiring] = useState<string | null>(null);
  // Follow the class in the fetched list: a refresh after a stale revision brings the revision the dialog must now
  // send. The dialog itself keeps the copy it was opened with until it is filled again (useSnapshot).
  const edited = editing?.kind === "edit" ? classes.find((item) => item.slug === editing.slug) : undefined;
  const retired = retiring === null ? undefined : classes.find((item) => item.slug === retiring);
  const full = classes.length >= 32;
  const add = (
    <Button
      variant="primary"
      size="md"
      onClick={() => setEditing({ kind: "create" })}
      disabled={full}
      title={full ? t("sourceClasses.limitReached") : undefined}
    >
      {t("sourceClasses.add")}
    </Button>
  );
  return (
    <>
      <Panel title={t("sourceClasses.listTitle")} action={add}>
        <p className="border-b border-b1 px-4 py-3 text-ui text-t2">{t("sourceClasses.intro")}</p>
        {classes.length === 0 ? (
          <EmptyState title={t("sourceClasses.empty")} action={add} />
        ) : (
          <ul className="divide-y divide-b1">
            {classes.map((item) => (
              <ClassRow
                key={item.slug}
                item={item}
                chinese={chinese}
                onEdit={() => setEditing({ kind: "edit", slug: item.slug })}
                onRetire={() => setRetiring(item.slug)}
              />
            ))}
          </ul>
        )}
      </Panel>
      {editing === null || (editing.kind === "edit" && edited === undefined) ? null : (
        <Editor key={edited?.slug ?? ""} cls={edited} onClose={() => setEditing(null)} />
      )}
      {retired === undefined ? null : <Retire key={retired.slug} cls={retired} onClose={() => setRetiring(null)} />}
    </>
  );
}

// ClassRow is one line of the list: the class, what it recognizes, and the two things that can be done to it.
function ClassRow({
  item,
  chinese,
  onEdit,
  onRetire,
}: {
  item: Class;
  chinese: boolean;
  onEdit: () => void;
  onRetire: () => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <li className="grid min-w-0 gap-3 px-4 py-3 lg:grid-cols-[minmax(0,1fr)_auto] lg:items-start">
      <div className="min-w-0 space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-label tabular-nums text-t4">
            {t("sourceClasses.position", { position: item.position })}
          </span>
          <span className="min-w-0 text-ui font-medium text-t1 [overflow-wrap:anywhere]">{nameOf(item, chinese)}</span>
          <Code>{item.slug}</Code>
          {item.system ? <State tone="neutral">{t("sourceClasses.system")}</State> : null}
          {item.fallback ? <State tone="neutral">{t("sourceClasses.fallback")}</State> : null}
        </div>
        <p className="text-label text-t2 [overflow-wrap:anywhere]">
          {item.description || t("sourceClasses.noDescription")}
        </p>
        <p className="text-label text-t3">
          {item.rules.conditions.length === 0
            ? t("sourceClasses.noRules")
            : `${t("sourceClasses.rulesCount", { count: item.rules.conditions.length })} · ${
                item.rules.match === "any" ? t("sourceClasses.matchAny") : t("sourceClasses.matchAll")
              }`}
        </p>
        <p className="text-label text-t4">
          {t("sourceClasses.updated", { at: format.dateTime(new Date(item.updated_at)) })}
        </p>
      </div>
      <div className="flex flex-wrap items-center gap-2 lg:justify-end">
        <Button size="sm" onClick={onEdit}>
          {t("sourceClasses.edit")}
        </Button>
        {item.system ? null : (
          <Button size="sm" onClick={onRetire}>
            {t("sourceClasses.retire")}
          </Button>
        )}
      </div>
    </li>
  );
}

function nameOf(item: Class, chinese: boolean) {
  // The Chinese name is what a Chinese page shows; without one, or in English, the English name is the name.
  return chinese ? item.name_zh || item.name : item.name;
}

function Editor({ cls, onClose }: { cls?: Class; onClose: () => void }) {
  const { t, i18n } = useTranslation();
  const [snapshot, reload] = useSnapshot(cls);
  const current = cls ? snapshot : undefined;
  const shown = current ? nameOf(current, i18n.language.startsWith("zh")) : "";
  return (
    <ChangeDialog<EditorInput, Class>
      title={current ? t("sourceClasses.editTitle", { name: shown }) : t("sourceClasses.createTitle")}
      operation={current ? `source-class:${current.slug}` : "source-class:new"}
      intro={current ? undefined : <p>{t("sourceClasses.intro")}</p>}
      action={current ? t("sourceClasses.edit") : t("sourceClasses.add")}
      reload={current ? reload : undefined}
      fields={() => <EditorFields current={current} />}
      read={(values) => readEditorInput(values, current, t)}
      send={(input, { reason, key }) => saveEditor(current, input, reason, key)}
      done={(saved) => <p>{t("sourceClasses.saved", { name: nameOf(saved, i18n.language.startsWith("zh")) })}</p>}
      onClose={onClose}
    />
  );
}

function readEditorInput(
  values: Record<string, string>,
  current: Class | undefined,
  t: TFunction<"admin">,
): EditorInput | Problem {
  const identity = readIdentity(values, current, t);
  if (isProblem(identity)) {
    return identity;
  }
  const rules = current?.fallback ? undefined : readRules(values, t);
  if (rules !== undefined && isProblem(rules)) {
    return rules;
  }
  return { ...identity, rules };
}

type Identity = {
  slug?: string;
  name: string;
  name_zh: string;
  description: string;
  position?: number;
};

function readIdentity(
  values: Record<string, string>,
  current: Class | undefined,
  t: TFunction<"admin">,
): Identity | Problem {
  const text = (key: string) => values[key]?.trim() ?? "";
  const name = text("name");
  if (name.length < 1 || name.length > 100) {
    return { problem: t("sourceClasses.invalidName") };
  }
  const slug = text("slug");
  if (!current && !/^[a-z][a-z0-9-]{0,31}$/.test(slug)) {
    return { problem: t("sourceClasses.invalidSlug") };
  }
  const position = readPosition(text("position"));
  if (position === null) {
    return { problem: t("sourceClasses.invalidPosition") };
  }
  return {
    slug: current ? undefined : slug,
    name,
    name_zh: text("name_zh"),
    description: text("description"),
    position,
  };
}

// readPosition is undefined for an empty field (keep the position, or append when creating) and null when invalid.
function readPosition(text: string): number | undefined | null {
  if (text === "") {
    return undefined;
  }
  const position = Number(text);
  return Number.isInteger(position) && position >= 0 && position <= 1_000_000 ? position : null;
}

function EditorFields({ current }: { current?: Class }) {
  const { t } = useTranslation();
  return (
    <>
      {current ? (
        <p className="text-ui text-t2">
          <Code>{current.slug}</Code>
        </p>
      ) : (
        <TextField label={t("sourceClasses.slug")} hint={t("sourceClasses.slugHint")} name="slug" maxLength={32} />
      )}
      <div className="grid gap-4 sm:grid-cols-2">
        <TextField label={t("sourceClasses.name")} name="name" defaultValue={current?.name} maxLength={100} required />
        <TextField label={t("sourceClasses.nameZh")} name="name_zh" defaultValue={current?.name_zh} maxLength={100} />
      </div>
      <TextAreaField
        label={t("sourceClasses.descriptionField")}
        hint={t("sourceClasses.descriptionHint")}
        name="description"
        defaultValue={current?.description}
        maxLength={500}
        rows={2}
        className="font-sans"
      />
      <TextField
        label={t("sourceClasses.positionField")}
        hint={t("sourceClasses.positionHint")}
        name="position"
        type="number"
        min={0}
        max={1_000_000}
        defaultValue={current?.position}
        disabled={current?.fallback}
      />
      {current?.fallback ? (
        <p className="text-ui text-t2">{t("sourceClasses.fallbackRules")}</p>
      ) : (
        <ConditionRows initial={current?.rules} />
      )}
    </>
  );
}

function saveEditor(current: Class | undefined, input: EditorInput, reason: string, key: string) {
  if (current) {
    return unwrapChange(
      adminApi.PUT("/api/admin/v1/source-classes/{slug}", {
        params: { path: { slug: current.slug }, header: { "Idempotency-Key": key } },
        body: {
          name: input.name,
          name_zh: input.name_zh,
          description: input.description,
          position: input.position,
          rules: input.rules,
          rev: current.revision,
          reason,
        },
      }),
    );
  }
  return unwrapChange(
    adminApi.POST("/api/admin/v1/source-classes", {
      params: { header: { "Idempotency-Key": key } },
      body: {
        slug: input.slug ?? "",
        name: input.name,
        name_zh: input.name_zh,
        description: input.description,
        position: input.position,
        rules: input.rules ?? { match: "all", conditions: [] },
        reason,
      },
    }),
  );
}

function Retire({ cls, onClose }: { cls: Class; onClose: () => void }) {
  const { t, i18n } = useTranslation();
  const [read, reload] = useSnapshot(cls);
  const shown = nameOf(read, i18n.language.startsWith("zh"));
  return (
    <ChangeDialog<{ rev: number }, null>
      title={t("sourceClasses.retireTitle", { name: shown })}
      operation={`source-class-retire:${cls.slug}`}
      intro={<p>{t("sourceClasses.retireIntro")}</p>}
      action={t("sourceClasses.retireAction")}
      danger
      confirm={read.slug}
      reload={reload}
      read={() => ({ rev: read.revision })}
      send={({ rev }, { reason, key }) =>
        unwrapEmptyChange(
          adminApi.DELETE("/api/admin/v1/source-classes/{slug}", {
            params: { path: { slug: read.slug }, header: { "Idempotency-Key": key } },
            body: { rev, reason },
          }),
        )
      }
      done={() => <p>{t("sourceClasses.retired", { name: shown })}</p>}
      onClose={onClose}
    />
  );
}
