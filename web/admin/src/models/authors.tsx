import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { can, useErrors, useSession } from "@axiom/app";
import { Button, DataState, EmptyState, Icon, Panel, Table, Td, TextField, Th } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "../change";
import { Code, mainCell } from "../parts";
import { authorsQuery } from "../queries";

type Author = AdminSchemas["Author"];

// The vendors whose models the catalog lists. A slug is the first part of a model's slug and never changes; only the
// name shown for it can.
export function Authors() {
  const { t } = useTranslation();
  const authors = useQuery(authorsQuery);
  const manage = can(useSession(), "catalog.manage");
  const [editing, setEditing] = useState<Author | "new" | null>(null);
  return (
    <Panel
      title={t("models.authors.title")}
      action={
        manage ? (
          <Button size="sm" onClick={() => setEditing("new")}>
            <Icon name="plus" />
            {t("models.authors.add")}
          </Button>
        ) : undefined
      }
    >
      <DataState
        query={authors}
        compact
        isEmpty={(data) => data.data.length === 0}
        empty={<EmptyState compact title={t("models.authors.empty")} />}
      >
        {(data) => <AuthorsTable authors={data.data} onEdit={manage ? setEditing : undefined} />}
      </DataState>
      {editing ? (
        <AuthorDialog author={editing === "new" ? undefined : editing} onClose={() => setEditing(null)} />
      ) : null}
    </Panel>
  );
}

function AuthorsTable({ authors, onEdit }: { authors: Author[]; onEdit: ((author: Author) => void) | undefined }) {
  const { t } = useTranslation();
  return (
    <Table label={t("models.authors.title")}>
      <thead>
        <tr>
          <Th>{t("models.authors.slug")}</Th>
          <Th>{t("models.authors.name")}</Th>
          {onEdit ? <Th className="w-0" /> : null}
        </tr>
      </thead>
      <tbody>
        {authors.map((author) => (
          <tr key={author.slug}>
            <Td className="max-w-48">
              <p className="truncate" title={author.slug}>
                <Code>{author.slug}</Code>
              </p>
            </Td>
            <Td className={mainCell}>
              <span className="block truncate" title={author.name}>
                {author.name}
              </span>
            </Td>
            {onEdit ? (
              <Td className="whitespace-nowrap">
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={t("models.authors.rename", { name: author.name })}
                  onClick={() => onEdit(author)}
                >
                  <Icon name="pencil" />
                </Button>
              </Td>
            ) : null}
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

type Save = { slug: string; name: string };

function AuthorDialog({ author, onClose }: { author?: Author; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const read = (values: Record<string, string>): Save | Problem => {
    const slug = author?.slug ?? (values.slug ?? "").trim();
    const name = (values.name ?? "").trim();
    if (!/^[a-z0-9][a-z0-9-]{0,39}$/.test(slug) || !name) {
      return { problem: t("models.authors.invalid") };
    }
    return { slug, name };
  };
  return (
    <ChangeDialog
      title={author ? t("models.authors.renameTitle") : t("models.authors.addTitle")}
      action={t("models.authors.save")}
      fields={(error) => (
        <>
          <TextField
            label={t("models.authors.slug")}
            hint={t("models.authors.slugHint")}
            name="slug"
            maxLength={40}
            autoComplete="off"
            spellCheck={false}
            defaultValue={author?.slug}
            readOnly={author !== undefined}
            error={errors.field(error, "slug")}
          />
          <TextField
            label={t("models.authors.name")}
            name="name"
            maxLength={100}
            autoComplete="off"
            defaultValue={author?.name}
            error={errors.field(error, "name")}
          />
        </>
      )}
      read={read}
      send={({ slug, name }, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/authors/{slug}", {
            params: { path: { slug }, header: { "Idempotency-Key": key } },
            body: { name, reason },
          }),
        )
      }
      done={(saved) => (
        <p>
          <Code>{saved.slug}</Code>
          {" · "}
          {saved.name}
        </p>
      )}
      onClose={onClose}
    />
  );
}
