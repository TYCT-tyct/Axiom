import { ApiError } from "@axiom/api";
import { Button, DataState, EmptyState, ModelCatalog, ModelFilterForm, PageHeader, Panel } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { type ModelFilters, modelsQuery } from "../queries";

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
