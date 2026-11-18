import type { AdminSchemas } from "@axiom/api";
import { LoadMore } from "@axiom/app";
import { ActionButton, Card, DataState, EmptyState, Field, Icon, PageHead, Table, Td } from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Authors } from "../models/authors";
import { modelStatus, modelStatuses } from "../models/catalog";
import { DraftDialog } from "../models/draft-dialog";
import { ModelState } from "../models/parts";
import { Actions, ChoiceFilter, Code, FilterBar, Head, NewButton, PageBody, text } from "../parts";
import { authorsQuery, type ModelFilters, modelsQuery } from "../queries";

export default function Models() {
  const { t } = useTranslation();
  const search = useSearch({ from: "/app/models" });
  const filters: ModelFilters = { status: search.status, author: search.author, slug_prefix: search.q };
  const models = useInfiniteQuery(modelsQuery(filters));
  const rows = models.data?.pages.flatMap((page) => page.data) ?? [];
  const filtered = Object.values(filters).some(Boolean);
  return (
    <>
      <PageHead title={t("nav.models")} lede={t("models.description")} />
      <PageBody>
        <Actions>
          <NewButton permission="catalog.manage" label={t("models.draft.open")}>
            {(close) => <DraftDialog onClose={close} />}
          </NewButton>
        </Actions>
        <Filter search={search} />
        <Card layout="flush">
          <DataState
            query={models}
            isEmpty={() => rows.length === 0}
            empty={
              <EmptyState title={filtered ? t("models.noMatch.title") : t("models.empty.title")}>
                {filtered ? t("models.noMatch.body") : t("models.empty.body")}
              </EmptyState>
            }
          >
            {() => <ModelsTable rows={rows} />}
          </DataState>
          <LoadMore list={models} />
        </Card>
        <Authors />
      </PageBody>
    </>
  );
}

type Search = { status?: string; author?: string; q?: string };

function Filter({ search }: { search: Search }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const authors = useQuery(authorsQuery);
  return (
    <FilterBar
      values={search}
      onApply={(values) =>
        navigate({
          to: "/models",
          search: { status: modelStatus(values.status), author: text(values.author), q: text(values.q) },
        })
      }
    >
      <ChoiceFilter
        label={t("models.col.status")}
        name="status"
        value={search.status}
        any={t("models.anyStatus")}
        options={modelStatuses.map((value) => ({ value, label: t(`models.state.${value}`) }))}
      />
      <ChoiceFilter
        label={t("models.col.author")}
        name="author"
        value={search.author}
        any={t("models.anyAuthor")}
        options={(authors.data?.data ?? []).map((author) => ({ value: author.slug, label: author.name }))}
      />
      <Field
        label={t("models.slugPrefix")}
        name="q"
        maxLength={200}
        defaultValue={search.q}
        autoComplete="off"
        spellCheck={false}
      />
    </FilterBar>
  );
}

function ModelsTable({ rows }: { rows: AdminSchemas["ModelSummary"][] }) {
  const { t } = useTranslation();
  return (
    <Table label={t("nav.models")}>
      <Head
        cols={[
          [t("models.col.slug"), { main: true }],
          t("models.col.status"),
          t("models.col.basis"),
          t("models.col.source"),
        ]}
        action
      />
      <tbody>
        {rows.map((model) => (
          <tr key={model.id}>
            <Td main>
              <Code>{model.slug}</Code>
            </Td>
            <Td>
              <ModelState status={model.status} />
            </Td>
            <Td>{t(`models.basis.${model.version_basis}`)}</Td>
            <Td>{t(`models.source.${model.price_source}`)}</Td>
            <Td actions>
              <ActionButton variant="gho" size="sm" square asChild>
                <Link
                  to="/models/$modelId"
                  params={{ modelId: model.id }}
                  aria-label={t("models.open", { slug: model.slug })}
                >
                  <Icon name="right" />
                </Link>
              </ActionButton>
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}
