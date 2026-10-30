import { ApiError } from "@axiom/api";
import { Button, DataState, EmptyState, ModelCatalog, PageHeader, Panel, SelectField, TextField } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { type ModelFilters, modelsQuery } from "../queries";

const abilities = [
  "tools",
  "images",
  "documents",
  "reasoning",
  "prompt_cache",
  "structured_output",
  "web_search",
  "code_execution",
] as const;

function filtersOf(form: HTMLFormElement): ModelFilters {
  const data = new FormData(form);
  const text = (name: string) => String(data.get(name) ?? "").trim() || undefined;
  const ability = text("ability") as ModelFilters["ability"];
  return {
    q: text("q"),
    author: text("author"),
    ability: ability && abilities.includes(ability) ? ability : undefined,
  };
}

export default function Models() {
  const { t } = useTranslation();
  const { t: common } = useTranslation("common");
  const filters: ModelFilters = useSearch({ from: "/app/models" });
  const models = useQuery(modelsQuery(filters));
  const navigate = useNavigate();
  const unavailable = models.error instanceof ApiError && models.error.status === 404;
  return (
    <>
      <PageHeader title={t("nav.models")} description={common("publicModels.description")} />
      <div className="space-y-4 p-4 sm:p-6">
        <Panel className="p-4">
          <ModelFilterForm filters={filters} onSubmit={(next) => void navigate({ to: "/models", search: next })} />
        </Panel>
        <Panel>
          {unavailable ? (
            <EmptyState title={common("publicModels.unavailableTitle")}>
              {common("publicModels.unavailableBody")}
            </EmptyState>
          ) : (
            <DataState
              query={models}
              isEmpty={(data) => data.data.length === 0}
              empty={
                <EmptyState title={common("publicModels.emptyTitle")}>{common("publicModels.emptyBody")}</EmptyState>
              }
            >
              {(data) => (
                <>
                  <p className="border-b border-b1 px-4 py-2 text-ui text-t3">
                    {common("publicModels.count", { count: data.data.length })}
                  </p>
                  <ModelCatalog models={data.data} note={common("publicModels.tierNote")} />
                </>
              )}
            </DataState>
          )}
        </Panel>
      </div>
    </>
  );
}

function ModelFilterForm({ filters, onSubmit }: { filters: ModelFilters; onSubmit: (filters: ModelFilters) => void }) {
  const { t } = useTranslation("common");
  return (
    <form
      key={JSON.stringify(filters)}
      noValidate
      className="grid gap-4 sm:grid-cols-2 xl:grid-cols-[repeat(3,minmax(0,1fr))_auto] xl:items-end"
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit(filtersOf(event.currentTarget));
      }}
    >
      <TextField
        label={t("publicModels.search")}
        name="q"
        maxLength={100}
        defaultValue={filters.q}
        placeholder={t("publicModels.searchHint")}
        autoComplete="off"
        spellCheck={false}
      />
      <TextField
        label={t("publicModels.author")}
        name="author"
        maxLength={100}
        defaultValue={filters.author}
        autoComplete="off"
        spellCheck={false}
      />
      <SelectField label={t("publicModels.ability")} name="ability" defaultValue={filters.ability ?? ""}>
        <option value="">{t("publicModels.allAbilities")}</option>
        {abilities.map((ability) => (
          <option key={ability} value={ability}>
            {t(`publicModels.abilityNames.${ability}`)}
          </option>
        ))}
      </SelectField>
      <div className="flex gap-2">
        <Button variant="primary" size="md" type="submit">
          {t("publicModels.apply")}
        </Button>
        <Button size="md" asChild>
          <Link to="/models" search={{ q: undefined, author: undefined, ability: undefined }}>
            {t("publicModels.reset")}
          </Link>
        </Button>
      </div>
    </form>
  );
}
