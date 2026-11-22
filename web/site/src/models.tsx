import { ApiError, consoleApi, unwrap } from "@axiom/api";
import { arrivedWhole } from "@axiom/fx";
import {
  Button,
  DataState,
  EmptyState,
  ModelCatalog,
  ModelFilterForm,
  type ModelFilters,
  modelAbilities,
  Panel,
  type PublicModel,
} from "@axiom/ui";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ModelsFrame } from "./home/models-frame";

function readFilters(): ModelFilters {
  const params = new URLSearchParams(window.location.search);
  const text = (name: string) => params.get(name)?.trim() || undefined;
  const ability = text("ability") as ModelFilters["ability"];
  return {
    q: text("q"),
    author: text("author"),
    ability: ability && modelAbilities.includes(ability) ? ability : undefined,
  };
}

function urlOf(filters: ModelFilters) {
  const params = new URLSearchParams();
  for (const [name, value] of Object.entries(filters)) {
    if (value) {
      params.set(name, value);
    }
  }
  const query = params.toString();
  return `${window.location.pathname}${query ? `?${query}` : ""}`;
}

type Data = { data: PublicModel[] };
type Query = {
  status: "pending" | "error" | "success";
  data: Data | undefined;
  error: unknown;
  errorUpdatedAt: number;
};

function usePublicModels(filters: ModelFilters, attempt: number) {
  const [query, setQuery] = useState<Query>({ status: "pending", data: undefined, error: null, errorUpdatedAt: 0 });
  useEffect(() => {
    const call = new AbortController();
    setQuery((previous) => ({ ...previous, status: "pending", error: null }));
    unwrap(consoleApi.GET("/api/console/v1/models", { params: { query: filters }, signal: call.signal }))
      .then((data) => {
        if (!call.signal.aborted) {
          setQuery({ status: "success", data, error: null, errorUpdatedAt: 0 });
        }
      })
      .catch((error: unknown) => {
        if (!call.signal.aborted) {
          setQuery({ status: "error", data: undefined, error, errorUpdatedAt: Date.now() });
        }
      });
    return () => call.abort();
  }, [filters, attempt]);
  return query;
}

export function Models() {
  const { t } = useTranslation("common");
  const [filters, setFilters] = useState<ModelFilters>(readFilters);
  const [attempt, setAttempt] = useState(0);
  const query = usePublicModels(filters, attempt);
  // drawn, the page is whole: the page left for it cross-fades into it now (crossing.ts)
  useEffect(() => arrivedWhole(), []);

  useEffect(() => {
    const back = () => setFilters(readFilters());
    window.addEventListener("popstate", back);
    return () => window.removeEventListener("popstate", back);
  }, []);

  const apply = (next: ModelFilters) => {
    setFilters(next);
    window.history.pushState(null, "", urlOf(next));
  };
  return (
    <ModelsFrame
      filters={
        <ModelFilterForm
          filters={filters}
          onApply={apply}
          reset={
            <Button size="md" type="button" onClick={() => apply({})}>
              {t("publicModels.reset")}
            </Button>
          }
        />
      }
    >
      <ModelResults query={query} onRetry={() => setAttempt((n) => n + 1)} onReset={() => apply({})} />
    </ModelsFrame>
  );
}

function ModelResults({ query, onRetry, onReset }: { query: Query; onRetry: () => void; onReset: () => void }) {
  const { t } = useTranslation("common");
  const unavailable = query.error instanceof ApiError && query.error.status === 404;
  return (
    <Panel id="model-results">
      {unavailable ? (
        <EmptyState title={t("publicModels.unavailableTitle")}>{t("publicModels.unavailableBody")}</EmptyState>
      ) : (
        <DataState
          query={{ ...query, refetch: onRetry }}
          isEmpty={(data) => data.data.length === 0}
          empty={
            <EmptyState
              title={t("publicModels.emptyTitle")}
              action={
                <Button type="button" onClick={onReset}>
                  {t("publicModels.reset")}
                </Button>
              }
            >
              {t("publicModels.emptyBody")}
            </EmptyState>
          }
        >
          {(data) => (
            <ModelCatalog
              models={data.data}
              count={t("publicModels.count", { count: data.data.length })}
              note={t("publicModels.note")}
            />
          )}
        </DataState>
      )}
    </Panel>
  );
}
