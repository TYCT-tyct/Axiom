import type { AdminSchemas } from "@axiom/api";
import { CodeBlock } from "@axiom/boards/ds";
import { Card, Cluster, DataTable, Tag, Text } from "@axiom/ui";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Lines } from "../lines";
import { Code, Facts, State } from "../parts";
import { otherFacts } from "./spec-fields";

type Model = AdminSchemas["Model"];

// A declared list: absent means the review has not been told yet, which keeps the model from going on sale; empty
// means it was told there is none.
function List({ values }: { values: string[] | undefined }) {
  const { t } = useTranslation();
  if (values === undefined) {
    return <State tone="warn">{t("models.spec.undeclared")}</State>;
  }
  if (values.length === 0) {
    return <Text tone="faint">{t("models.spec.none")}</Text>;
  }
  return (
    <Cluster gap={4}>
      {values.map((value) => (
        <Tag key={value}>{value}</Tag>
      ))}
    </Cluster>
  );
}

export function SpecPanel({ model }: { model: Model }) {
  const { t } = useTranslation();
  const { items, tiers } = model.spec;
  const other = otherFacts(model.spec);
  return (
    <Facts
      title={t("models.spec.title")}
      rows={[
        [t("models.spec.items"), <List key="items" values={items?.map((item) => t(`models.item.${item}`))} />],
        [t("models.spec.tiers"), <List key="tiers" values={tiers?.map((tier) => t(`models.tier.${tier}`))} />],
        ...(other
          ? [
              [
                t("models.spec.other"),
                <CodeBlock key="other" label={t("models.spec.other")}>
                  {other}
                </CodeBlock>,
              ] as [string, ReactNode],
            ]
          : []),
      ]}
    />
  );
}

export function NamesPanel({ model }: { model: Model }) {
  const { t } = useTranslation();
  return (
    <Card layout="flush" title={t("models.names.title")}>
      <DataTable
        label={t("models.names.title")}
        rows={model.names}
        rowKey={(name) => name.name}
        columns={[
          { key: "name", header: t("models.names.name"), cell: (name) => <Code>{name.name}</Code> },
          {
            key: "visibility",
            header: t("models.names.visibility"),
            cell: (name) => (name.visibility === "public" ? t("models.names.public") : t("models.names.internal")),
          },
        ]}
      />
    </Card>
  );
}

const costTones = { known: "ok", unknown: "warn", conflict: "bad" } as const;

// LinesPanel lists where the model runs: each endpoint with the line it is on, its name at the upstream and whether it
// can take a request. A session that may not read the suppliers has no such list.
export function LinesPanel({ model, lines }: { model: Model; lines: Lines }) {
  const { t } = useTranslation();
  if (!lines.allowed || !lines.ready) {
    return null;
  }
  const endpoints = lines.endpointsOf(model.id);
  return (
    <Card layout="flush" title={t("models.lines.title")}>
      {endpoints.length === 0 ? (
        <Text tone="muted">{t("models.lines.empty")}</Text>
      ) : (
        <DataTable
          label={t("models.lines.title")}
          rows={endpoints}
          rowKey={({ endpoint }) => endpoint.id}
          columns={[
            {
              key: "line",
              header: t("models.lines.title"),
              cell: ({ endpoint, line }) => line?.name ?? endpoint.group_id,
            },
            {
              key: "upstream",
              header: t("models.lines.upstreamName"),
              cell: ({ endpoint }) => <Code>{endpoint.upstream_name}</Code>,
            },
            {
              key: "state",
              header: t("models.col.status"),
              cell: ({ endpoint }) =>
                endpoint.enabled ? (
                  <Tag tone={costTones[endpoint.cost_state]} dot>
                    {t(
                      `models.lines.cost${endpoint.cost_state === "known" ? "Known" : endpoint.cost_state === "unknown" ? "Unknown" : "Conflict"}`,
                    )}
                  </Tag>
                ) : (
                  <Tag dot>{t("models.lines.stoppedTag")}</Tag>
                ),
            },
          ]}
        />
      )}
    </Card>
  );
}
