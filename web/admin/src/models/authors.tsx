import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { can, useErrors, useSession } from "@axiom/app";
import { ActionButton, Card, DataState, EmptyState, Field, Table, Td } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "../change";
import { Code, Head } from "../parts";
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
    <Card
      layout="flush"
      title={t("models.authors.title")}
      actions={
        manage ? (
          <ActionButton size="sm" icon="plus" onClick={() => setEditing("new")}>
            {t("models.authors.add")}
          </ActionButton>
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
    </Card>
  );
}

function AuthorsTable({ authors, onEdit }: { authors: Author[]; onEdit: ((author: Author) => void) | undefined }) {
  const { t } = useTranslation();
  return (
    <Table label={t("models.authors.title")}>
      <Head
        cols={[t("models.authors.slug"), [t("models.authors.name"), { main: true }]]}
        action={onEdit !== undefined}
      />
      <tbody>
        {authors.map((author) => (
          <tr key={author.slug}>
            <Td>
              <Code>{author.slug}</Code>
            </Td>
            <Td main>{author.name}</Td>
            {onEdit ? (
              <Td shrink>
                <ActionButton
                  variant="gho"
                  size="sm"
                  icon="pencil"
                  aria-label={t("models.authors.rename", { name: author.name })}
                  onClick={() => onEdit(author)}
                />
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
      operation={author ? `model-author:edit:${author.slug}` : "model-author:create"}
      title={author ? t("models.authors.renameTitle") : t("models.authors.addTitle")}
      action={t("models.authors.save")}
      fields={(error) => (
        <>
          <Field
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
          <Field
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
