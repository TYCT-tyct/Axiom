import type { AdminSchemas } from "@axiom/api";
import { ActionButton, Card, Cluster, DataTable, Facts, Note, SidePanel, Stack } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { Code } from "../parts";
import { Margin, type PreviewRow, Price } from "./preview";
import { PublicationAudience } from "./publications";
import { Standings, standingOf } from "./standing";

type Publication = AdminSchemas["PricePublication"];

// PriceDrawer is a model's sale price over the list (adP_pub in admin_pages_b.js): what the rule asks, what the floor
// lets it be, what it costs and what it sells for now, and what can be done: publish it, publish it at the floor, or
// go and change the rule. Every price is the server's; when it prices endpoints apart they are listed one by one.
export function PriceDrawer({
  publication,
  slug,
  preview,
  manage,
  index,
  count,
  onStep,
  onPublish,
  onRules,
  onClose,
}: {
  publication: Publication;
  slug: string;
  preview: readonly PreviewRow[];
  manage: boolean;
  index: number;
  count: number;
  onStep: (by: -1 | 1) => void;
  onPublish: () => void;
  onRules: () => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const standing = standingOf(publication);
  // One endpoint's cost and margin stand for the model; several are in the list of endpoints.
  const only = preview.length === 1 ? preview[0] : undefined;
  const publishable = manage && publication.candidate_id !== undefined && publication.model_id !== undefined;
  return (
    <SidePanel
      drawer
      wide
      title={slug}
      subtitle={t("pricing.sale.unit")}
      onClose={onClose}
      steps={{
        index,
        count,
        onStep,
        previousLabel: t("pricing.sale.previous"),
        nextLabel: t("pricing.sale.next"),
      }}
      footer={
        standing === "floor" || standing === "waiting" ? (
          <Cluster justify="end">
            {standing === "floor" ? <ActionButton onClick={onRules}>{t("pricing.sale.seeRules")}</ActionButton> : null}
            {publishable ? (
              <ActionButton variant="pri" onClick={onPublish}>
                {standing === "floor" ? t("pricing.publish.atFloor") : t("pricing.publish.action")}
              </ActionButton>
            ) : null}
          </Cluster>
        ) : undefined
      }
    >
      <Stack>
        {standing === "floor" ? <Note title={t("pricing.sale.floorTitle")}>{t("pricing.sale.floorBody")}</Note> : null}
        <Facts
          rows={[
            [t("pricing.publish.state"), <Standings key="state" row={publication} />],
            [t("pricing.rules.audience"), <PublicationAudience key="audience" audience={publication.audience} />],
            [t("pricing.sale.cost"), <Price key="cost" schedule={only?.cost} />],
            [t("pricing.sale.current"), <Price key="current" schedule={publication.current_schedule} />],
            [t("pricing.publish.requested"), <Price key="requested" schedule={publication.requested} />],
            [t("pricing.sale.new"), <Price key="new" schedule={publication.schedule} />],
            [t("pricing.sale.floor"), <Price key="floor" schedule={publication.floor} />],
            [t("pricing.sale.margin"), only ? <Margin key="margin" row={only} /> : "—"],
            [
              t("pricing.publish.version"),
              publication.current_id ? <Code key="id">{publication.current_id}</Code> : "—",
            ],
          ]}
        />
        <Endpoints publication={publication} preview={preview} />
      </Stack>
    </SidePanel>
  );
}

// When a proposal prices endpoints apart, each with its cost, what it sells for now, what the rule asks, its new price
// and its floor, as the server gives them.
function Endpoints({ publication, preview }: { publication: Publication; preview: readonly PreviewRow[] }) {
  const { t } = useTranslation();
  const { costs, current_endpoints: current, requested_endpoints: requested, endpoints, floors } = publication;
  const ids = [
    ...new Set([
      ...[costs, current, requested, endpoints, floors].flatMap((prices) => Object.keys(prices ?? {})),
      ...preview.flatMap((row) => (row.endpoint_id ? [row.endpoint_id] : [])),
    ]),
  ];
  const rowOf = (id: string) => preview.find((row) => row.endpoint_id === id);
  if (ids.length === 0) return null;
  return (
    <Card layout="flush" title={t("pricing.sale.endpoints")}>
      <DataTable
        label={t("pricing.sale.endpoints")}
        rows={ids}
        rowKey={(id) => id}
        columns={[
          { key: "id", header: t("pricing.sale.endpoint"), cell: (id) => <Code>{id}</Code> },
          {
            key: "cost",
            header: t("pricing.sale.cost"),
            cell: (id) => <Price schedule={costs?.[id] ?? rowOf(id)?.cost} />,
          },
          { key: "current", header: t("pricing.sale.current"), cell: (id) => <Price schedule={current?.[id]} /> },
          {
            key: "requested",
            header: t("pricing.publish.requested"),
            cell: (id) => <Price schedule={requested?.[id]} />,
          },
          { key: "new", header: t("pricing.sale.new"), cell: (id) => <Price schedule={endpoints?.[id]} /> },
          { key: "floor", header: t("pricing.sale.floor"), cell: (id) => <Price schedule={floors?.[id]} /> },
          {
            key: "margin",
            header: t("pricing.sale.margin"),
            cell: (id) => {
              const row = rowOf(id);
              return row ? <Margin row={row} /> : "—";
            },
          },
        ]}
      />
    </Card>
  );
}
