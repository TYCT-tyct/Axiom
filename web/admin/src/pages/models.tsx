import type { AdminSchemas } from "@axiom/api";
import { LoadMore } from "@axiom/app";
import { Button, DataState, EmptyState, Icon, PageHeader, Panel, Table, Td, TextField, Th } from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Authors } from "../models/authors";
import { modelStatus, modelStatuses } from "../models/catalog";
import { DraftDialog } from "../models/draft-dialog";
import { ModelState } from "../models/parts";
import { Actions, ChoiceFilter, Code, FilterBar, NewButton, PageBody, text } from "../parts";
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
      <PageHeader title={t("nav.models")} description={t("models.description")} />
      <PageBody>
        <Actions>
          <NewButton permission="catalog.manage" label={t("models.draft.open")}>
            {(close) => <DraftDialog onClose={close} />}
          </NewButton>
        </Actions>
        <Filter search={search} />
        <Panel>
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
        </Panel>
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
      <TextField
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
      <thead>
        <tr>
          <Th>{t("models.col.slug")}</Th>
          <Th>{t("models.col.status")}</Th>
          <Th className="hidden md:table-cell">{t("models.col.basis")}</Th>
          <Th className="hidden lg:table-cell">{t("models.col.source")}</Th>
          <Th className="w-0" />
        </tr>
      </thead>
      <tbody>
        {rows.map((model) => (
          <tr key={model.id}>
            <Td className="w-full max-w-0 sm:w-auto sm:max-w-80 sm:min-w-40">
              <p className="truncate text-t1" title={model.slug}>
                <Code>{model.slug}</Code>
              </p>
            </Td>
            <Td>
              <ModelState status={model.status} />
            </Td>
            <Td className="hidden md:table-cell">{t(`models.basis.${model.version_basis}`)}</Td>
            <Td className="hidden lg:table-cell">{t(`models.source.${model.price_source}`)}</Td>
            <Td className="whitespace-nowrap">
              <Button variant="ghost" size="icon" asChild>
                <Link
                  to="/models/$modelId"
                  params={{ modelId: model.id }}
                  aria-label={t("models.open", { slug: model.slug })}
                >
                  <Icon name="right" />
                </Link>
              </Button>
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}
