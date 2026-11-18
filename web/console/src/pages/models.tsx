import { ApiError } from "@axiom/api";
import ModelsCard from "@axiom/boards/AxApp/regions/ModelsCard";
import ModelsFrame from "@axiom/boards/AxApp/regions/ModelsFrame";
import { PageHeader } from "@axiom/boards/ds";
import { Button, DataState, EmptyState, ModelCatalog, ModelFilterForm, usePageTitle } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Announcements } from "../announcements";
import { type ModelFilters, modelsQuery } from "../queries";

export default function Models() {
  const { t } = useTranslation();
  const { t: common } = useTranslation("common");
  const filters: ModelFilters = useSearch({ from: "/app/models" });
  const models = useQuery(modelsQuery(filters));
  const navigate = useNavigate();
  const unavailable = models.error instanceof ApiError && models.error.status === 404;
  const title = t("nav.models");
  usePageTitle(title);
  return (
    <ModelsFrame t={String}>
      <PageHeader title={title} description={common("publicModels.description")} />
      <Announcements />
      <ModelsCard t={String}>
        <ModelFilterForm
          filters={filters}
          onApply={(next) => void navigate({ to: "/models", search: next })}
          reset={
            <Button size="md" asChild>
              <Link to="/models" search={{ q: undefined, author: undefined, ability: undefined }}>
                {common("publicModels.reset")}
              </Link>
            </Button>
          }
        />
      </ModelsCard>
      <ModelsCard t={String}>
        {unavailable ? (
          <EmptyState
            title={common("publicModels.unavailableTitle")}
            action={<Button onClick={() => models.refetch()}>{common("state.retry")}</Button>}
          >
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
                <p>{common("publicModels.count", { count: data.data.length })}</p>
                <ModelCatalog models={data.data} note={common("publicModels.tierNote")} />
              </>
            )}
          </DataState>
        )}
      </ModelsCard>
    </ModelsFrame>
  );
}
