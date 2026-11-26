import { can, LoadMore, useSession } from "@axiom/app";
import { ActionButton, Card, Cluster, DataState, EmptyState, Field } from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useLines } from "../lines";
import { Authors } from "../models/authors";
import { modelStatus, modelStatuses } from "../models/catalog";
import { CatalogTable } from "../models/catalog-table";
import { DraftDialog } from "../models/draft-dialog";
import { ModelDrawer } from "../models/drawer";
import { ImportDialog } from "../models/library-dialogs";
import { ChoiceFilter, FilterBar, NewButton, text } from "../parts";
import { authorsQuery, type ModelFilters, modelsQuery } from "../queries";
import { TabbedPage, useTabs } from "../tabbed-page";

type Search = { status?: string; author?: string; q?: string; model?: string };

// The catalog and the authors of its models (adPg_models in admin_pages_a.js). A model opens in a drawer over the
// catalog, which the address names (`?model=`), so a link to a model is a link to the catalog with it open.
export default function Models() {
  const { t } = useTranslation();
  const search = useSearch({ from: "/app/models" });
  const tab: "catalog" | "authors" = search.tab ?? "catalog";
  const tabs = useTabs(
    "/models",
    [
      { key: "catalog", label: t("models.tabs.catalog") },
      { key: "authors", label: t("models.tabs.authors") },
    ],
    tab,
  );
  const manage = can(useSession(), "catalog.manage");
  return (
    <TabbedPage
      title={t("nav.models")}
      lede={tab === "catalog" ? t("models.description") : undefined}
      label={t("models.tabs.label")}
      tabs={tabs}
      actions={
        tab === "catalog" && manage ? (
          <Cluster>
            <ImportButton />
            <NewButton permission="catalog.manage" label={t("models.draft.open")}>
              {(close) => <DraftDialog onClose={close} />}
            </NewButton>
          </Cluster>
        ) : undefined
      }
    >
      {tab === "authors" ? <Authors /> : <Catalog search={search} />}
    </TabbedPage>
  );
}

function ImportButton() {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  return (
    <>
      <ActionButton onClick={() => setOpen(true)}>{t("models.import.open")}</ActionButton>
      {open ? <ImportDialog onClose={() => setOpen(false)} /> : null}
    </>
  );
}

function Catalog({ search }: { search: Search }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const filters: ModelFilters = { status: modelStatus(search.status), author: search.author, slug_prefix: search.q };
  const models = useInfiniteQuery(modelsQuery(filters));
  const authors = useQuery(authorsQuery);
  const lines = useLines();
  const rows = models.data?.pages.flatMap((page) => page.data) ?? [];
  const filtered = Object.values(filters).some(Boolean);
  const names = new Map((authors.data?.data ?? []).map((author) => [author.slug, author.name]));
  const selected = (id: string | undefined, replace = false) =>
    void navigate({ to: "/models", search: { ...search, model: id }, replace });
  const index = rows.findIndex((row) => row.id === search.model);
  return (
    <>
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
          {() => <CatalogTable rows={rows} authors={names} lines={lines} onOpen={selected} />}
        </DataState>
        <LoadMore list={models} />
      </Card>
      {search.model ? (
        <ModelDrawer
          id={search.model}
          index={index}
          count={rows.length}
          slug={rows[index]?.slug}
          authors={names}
          lines={lines}
          onStep={(by) => selected(rows[index + by]?.id, true)}
          onClose={() => selected(undefined)}
        />
      ) : null}
    </>
  );
}

function Filter({ search }: { search: Search }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const authors = useQuery(authorsQuery);
  return (
    <FilterBar
      values={{ status: search.status, author: search.author, q: search.q }}
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
