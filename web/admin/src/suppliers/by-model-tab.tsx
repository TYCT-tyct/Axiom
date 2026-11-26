import { type AdminSchemas, adminApi, unwrap } from "@axiom/api";
import { accountKey, can, useSession } from "@axiom/app";
import {
  Card,
  Cluster,
  DataState,
  DataTable,
  EmptyState,
  LinkButton,
  PaceBar,
  RowList,
  Switch,
  Tag,
  Text,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery, useQueries } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { type Line, type Lines, useLines } from "../lines";
import { showRate } from "../models/price-fields";
import { Code } from "../parts";
import { costShare, type ModelView, modelView, tokenPrice } from "./by-model";
import { EndpointEnabledDialog } from "./dialogs";
import { EndpointDrawer } from "./endpoint-drawer";
import { Rate, useTraffic } from "./parts";
import { listedModelsQuery } from "./queries";

type Model = AdminSchemas["ModelSummary"];
type Endpoint = AdminSchemas["SupplierEndpoint"];
type Schedule = AdminSchemas["PriceSchedule"];
type Served = { endpoint: Endpoint; line: Line | undefined };
type Opened = { kind: "drawer" | "switch"; endpoint: Endpoint; line: Line };

// The preview answers for at most 100 models at a time.
const chunk = <T,>(all: readonly T[], size: number) =>
  Array.from({ length: Math.ceil(all.length / size) }, (_, at) => all.slice(at * size, at * size + size));

// 按模型 (adUpModels): a row per model on sale, with how many lines serve it, the lowest cost, the sale price and the
// margin; opened, its lines, cheapest first, with each cost as a bar against the sale price. The costs and the sale
// are the pricing preview's (previewPricing), one row per endpoint, in the site's currency.
export default function ByModelTab() {
  const { t } = useTranslation();
  const models = useInfiniteQuery(listedModelsQuery);
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = models;
  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage) void fetchNextPage();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);
  const rows = models.data?.pages.flatMap((page) => page.data) ?? [];
  const lines = useLines();
  const views = useViews(rows, lines);
  const [expanded, setExpanded] = useState<string>();
  const [opened, setOpened] = useState<Opened | null>(null);
  return (
    <Card layout="flush">
      <DataState
        query={models}
        isEmpty={() => rows.length === 0}
        empty={<EmptyState title={t("upstream.byModel.empty.title")}>{t("upstream.byModel.empty.body")}</EmptyState>}
      >
        {() => (
          <>
            <DataTable
              label={t("upstream.tabs.models")}
              rows={rows}
              rowKey={(row) => row.id}
              onOpen={(row) => setExpanded((was) => (was === row.id ? undefined : row.id))}
              expanded={expanded}
              detail={(row) => (
                <ModelLines served={lines.endpointsOf(row.id)} view={views.get(row.id)} onOpen={setOpened} />
              )}
              columns={columns(
                {
                  model: t("upstream.byModel.col.model"),
                  lines: t("upstream.byModel.col.lines"),
                  cost: t("upstream.byModel.col.cost"),
                  sale: t("upstream.byModel.col.sale"),
                  margin: t("upstream.byModel.col.margin"),
                  cheapest: t("upstream.byModel.col.cheapest"),
                },
                lines,
                views,
              )}
            />
            <Text size="sm" tone="muted">
              {t("upstream.byModel.foot")}
            </Text>
          </>
        )}
      </DataState>
      {opened ? <Opened opened={opened} onClose={() => setOpened(null)} /> : null}
    </Card>
  );
}

// The preview of every model listed, asked a hundred at a time; a model with no row yet has no view.
function useViews(models: readonly Model[], lines: Lines) {
  const allowed = can(useSession(), "pricing.view");
  const previews = useQueries({
    queries: chunk(
      models.map((model) => model.id),
      100,
    ).map((ids) => ({
      queryKey: [accountKey, "preview", { model_ids: ids }],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        unwrap(adminApi.POST("/api/admin/v1/pricing/previews", { body: { model_ids: ids }, signal })),
      enabled: allowed,
    })),
  });
  const rows = previews.flatMap((preview) => preview.data?.data ?? []);
  return new Map(
    models.map((model) => [
      model.id,
      modelView(
        rows.filter((row) => row.model_id === model.id),
        (id) => inUse(lines, model.id, id),
      ),
    ]),
  );
}

const inUse = (lines: Lines, modelId: string, endpointId: string) =>
  lines
    .endpointsOf(modelId)
    .some(({ endpoint, line }) => endpoint.id === endpointId && endpoint.enabled && line?.usable);

function PerMillion({ schedule }: { schedule: Schedule | undefined }) {
  const { decimal } = useFormat();
  const input = schedule?.rates.input;
  const output = schedule?.rates.output;
  if (input === undefined && output === undefined) return <>{"—"}</>;
  const shown = (item: "input" | "output", price: string | undefined) =>
    price === undefined ? "—" : decimal(showRate(item, price));
  return (
    <Text as="span" numeric nowrap>
      {`${shown("input", input)} / ${shown("output", output)}`}
    </Text>
  );
}

type Headers = Record<"model" | "lines" | "cost" | "sale" | "margin" | "cheapest", string>;

function columns(headers: Headers, lines: Lines, views: Map<string, ModelView>) {
  const live = (id: string) => lines.endpointsOf(id).filter(({ endpoint, line }) => endpoint.enabled && line?.usable);
  return [
    {
      key: "model",
      header: headers.model,
      cell: (row: Model) => <Code>{row.slug}</Code>,
    },
    {
      key: "lines",
      header: headers.lines,
      from: "md" as const,
      sort: (row: Model) => live(row.id).length,
      cell: (row: Model) => `${live(row.id).length} / ${lines.endpointsOf(row.id).length}`,
    },
    {
      key: "cost",
      header: headers.cost,
      align: "end" as const,
      cell: (row: Model) => <PerMillion schedule={views.get(row.id)?.cheapest?.cost} />,
    },
    {
      key: "sale",
      header: headers.sale,
      align: "end" as const,
      from: "lg" as const,
      cell: (row: Model) => <PerMillion schedule={views.get(row.id)?.sale} />,
    },
    {
      key: "margin",
      header: headers.margin,
      align: "end" as const,
      sort: (row: Model) => margin(views.get(row.id)),
      cell: (row: Model) => <Margin value={margin(views.get(row.id))} />,
    },
    {
      key: "cheapest",
      header: headers.cheapest,
      from: "md" as const,
      cell: (row: Model) => <Cheapest view={views.get(row.id)} served={lines.endpointsOf(row.id)} />,
    },
  ];
}

const margin = (view: ModelView | undefined) => {
  const ratio = view?.cheapest?.margin_ratio;
  return ratio === undefined ? undefined : Number(ratio) * 100;
};

// A margin under this is shown as one to watch (adUpModels: under 15%).
const thinMargin = 15;

function Margin({ value }: { value: number | undefined }) {
  if (value === undefined) return <>{"—"}</>;
  const text = `${value.toFixed(1)}%`;
  return value < thinMargin ? (
    <Text as="span" tone={value < 0 ? "bad" : "warn"} numeric>
      {text}
    </Text>
  ) : (
    text
  );
}

function Cheapest({ view, served }: { view: ModelView | undefined; served: Served[] }) {
  const { t } = useTranslation();
  const best = served.find(({ endpoint }) => endpoint.id === view?.cheapest?.endpoint_id);
  if (best?.line) return <>{best.line.name}</>;
  const live = served.some(({ endpoint, line }) => endpoint.enabled && line?.usable);
  return (
    <Text as="span" tone={live ? "muted" : "warn"}>
      {t(live ? "upstream.byModel.perRequest" : "upstream.byModel.noLine")}
    </Text>
  );
}

// A model's lines, the ones taking requests first and the cheapest of them first.
function ModelLines({
  served,
  view,
  onOpen,
}: {
  served: Served[];
  view: ModelView | undefined;
  onOpen: (opened: Opened) => void;
}) {
  const { t } = useTranslation();
  const traffic = useTraffic();
  const manage = can(useSession(), "suppliers.manage");
  if (served.length === 0) {
    return <Text tone="muted">{t("upstream.byModel.none")}</Text>;
  }
  const cost = (endpoint: Endpoint) => tokenPrice(view?.endpoints.get(endpoint.id)?.cost) ?? Number.POSITIVE_INFINITY;
  const order = served.toSorted(
    (a, b) => Number(b.endpoint.enabled) - Number(a.endpoint.enabled) || cost(a.endpoint) - cost(b.endpoint),
  );
  return (
    <RowList
      label={t("upstream.byModel.col.lines")}
      items={order.map(({ endpoint, line }) => ({
        key: endpoint.id,
        title: line?.name ?? endpoint.group_id.slice(0, 8),
        sub: <LineCost row={view?.endpoints.get(endpoint.id)} sale={view?.sale} cheapest={view?.cheapest} />,
        end: (
          <Cluster gap={12}>
            {endpoint.enabled ? (
              <Rate rate={traffic?.ofGroup(endpoint.group_id).rate} />
            ) : (
              <Text as="span" tone="muted">
                {t("upstream.byModel.off")}
              </Text>
            )}
            {line ? (
              <LinkButton
                label={t("upstream.endpoint.open", { name: line.name })}
                onClick={() => onOpen({ kind: "drawer", endpoint, line })}
              >
                {t("upstream.byModel.details")}
              </LinkButton>
            ) : null}
            {line && manage ? (
              <Switch
                label={t("upstream.endpoint.open", { name: line.name })}
                checked={endpoint.enabled}
                onChange={() => onOpen({ kind: "switch", endpoint, line })}
              />
            ) : null}
          </Cluster>
        ),
      }))}
    />
  );
}

function LineCost({
  row,
  sale,
  cheapest,
}: {
  row: AdminSchemas["PricingPreviewRow"] | undefined;
  sale: Schedule | undefined;
  cheapest: AdminSchemas["PricingPreviewRow"] | undefined;
}) {
  const { t } = useTranslation();
  const share = costShare(row?.cost, sale);
  if (share === undefined) {
    return <>{t("upstream.byModel.perRequest")}</>;
  }
  return (
    <Cluster gap={8}>
      <PaceBar
        label={t("upstream.byModel.costBar")}
        value={Math.min(share, 100)}
        max={100}
        warn={share > 100}
        width={96}
      />
      <PerMillion schedule={row?.cost} />
      {row?.endpoint_id === cheapest?.endpoint_id ? <Tag tone="ok">{t("upstream.byModel.cheapestTag")}</Tag> : null}
      {share > 100 ? <Tag tone="bad">{t("upstream.byModel.above")}</Tag> : null}
    </Cluster>
  );
}

function Opened({ opened, onClose }: { opened: Opened; onClose: () => void }) {
  const { endpoint, line } = opened;
  if (opened.kind === "switch") {
    return <EndpointEnabledDialog supplier={line.supplier} endpoint={endpoint} group={line.name} onClose={onClose} />;
  }
  return <EndpointDrawer supplier={line.supplier} endpoint={endpoint} line={line.name} onClose={onClose} />;
}
