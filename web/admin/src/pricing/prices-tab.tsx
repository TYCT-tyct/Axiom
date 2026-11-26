import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { ActionButton, Card, Cluster, DataState, DataTable, EmptyState, LinkButton, Tag, Text } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { listedModelsQuery, previewLimit, publicationsQuery } from "../queries";
import { Margin, type PreviewRow, Price, usePreview } from "./preview";
import { PriceDrawer } from "./price-drawer";
import { publicationKey } from "./publication-key";
import { PublishDialog } from "./publications";
import { Standings, standingOf } from "./standing";

type Publication = AdminSchemas["PricePublication"];

// The publications, the names of their models, and the preview of those models the rows take their cost and margin from.
function usePrices() {
  const publications = useQuery(publicationsQuery);
  const listed = useQuery(listedModelsQuery);
  const rows = publications.data?.data ?? [];
  const slugs = new Map((listed.data?.data ?? []).map((model) => [model.id, model.slug]));
  // The preview is of the models the publications are of, for the site's default audience: its cost and margin go to
  // the rows of everyone, and a row of a tier or an account has none of its own here.
  const models = [...new Set(rows.flatMap((row) => (row.model_id ? [row.model_id] : [])))].slice(0, previewLimit);
  const preview = usePreview(models.length > 0 ? { model_ids: models } : null);
  // A model has a row for each endpoint it runs on.
  const priced = new Map<string, PreviewRow[]>();
  for (const row of preview.data?.data ?? []) {
    priced.set(row.model_id, [...(priced.get(row.model_id) ?? []), row]);
  }
  return {
    publications,
    rows,
    slugOf: (row: Publication) => (row.model_id ? (slugs.get(row.model_id) ?? row.model_id) : "—"),
    previewOf: (row: Publication): readonly PreviewRow[] =>
      row.model_id && (!row.audience || row.audience.kind === "all") ? (priced.get(row.model_id) ?? []) : [],
  };
}

// PricesTab is every model's sale price next to the proposal for it (adPg_pricing in admin_pages_b.js): what it costs,
// what it sells for now, what the rules would sell it for, the margin that gives, and why a proposal could not be
// made. Only what the publication list and a preview of the same models say is shown. A price the rule asks below the
// floor can only be published at the floor, or be sent to the rules to change.
export function PricesTab({ selected, onRules }: { selected: string | undefined; onRules: () => void }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const manage = can(useSession(), "pricing.manage");
  const { publications, rows, slugOf, previewOf } = usePrices();
  const [publishing, setPublishing] = useState<string>();
  const open = (key: string | undefined, replace = false) =>
    void navigate({ to: "/pricing", search: { tab: undefined, price: key }, replace });
  const index = rows.findIndex((row) => publicationKey(row) === selected);
  const chosen = rows[index];
  const publish = rows.find((row) => publicationKey(row) === publishing);
  return (
    <>
      <DataState
        query={publications}
        isEmpty={(data) => data.data !== undefined && data.data.length === 0}
        empty={<EmptyState title={t("pricing.sale.empty")} />}
      >
        {(data) =>
          data.data ? (
            <>
              <Summary rows={data.data} />
              <Card layout="flush">
                <PricesTable
                  rows={data.data}
                  slugOf={slugOf}
                  previewOf={previewOf}
                  manage={manage}
                  onOpen={(row) => open(publicationKey(row))}
                  onPublish={(row) => setPublishing(publicationKey(row))}
                />
              </Card>
            </>
          ) : (
            <Text tone="faint">{t("pricing.publish.unavailable")}</Text>
          )
        }
      </DataState>
      {chosen ? (
        <PriceDrawer
          publication={chosen}
          slug={slugOf(chosen)}
          preview={previewOf(chosen)}
          manage={manage}
          index={index}
          count={rows.length}
          onStep={(by) => {
            const next = rows[index + by];
            if (next) open(publicationKey(next), true);
          }}
          onPublish={() => setPublishing(publicationKey(chosen))}
          onRules={() => {
            open(undefined);
            onRules();
          }}
          onClose={() => open(undefined)}
        />
      ) : null}
      {publish ? <PublishDialog live={publish} onClose={() => setPublishing(undefined)} /> : null}
    </>
  );
}

function PricesTable({
  rows,
  slugOf,
  previewOf,
  manage,
  onOpen,
  onPublish,
}: {
  rows: readonly Publication[];
  slugOf: (row: Publication) => string;
  previewOf: (row: Publication) => readonly PreviewRow[];
  manage: boolean;
  onOpen: (row: Publication) => void;
  onPublish: (row: Publication) => void;
}) {
  const { t } = useTranslation();
  return (
    <DataTable
      fixed
      label={t("pricing.publish.title")}
      rows={rows}
      rowKey={publicationKey}
      columns={[
        {
          key: "model",
          width: 260,
          header: t("pricing.publish.model"),
          cell: (row) => <ModelCell row={row} name={slugOf(row)} onOpen={() => onOpen(row)} />,
        },
        {
          key: "cost",
          width: 160,
          header: t("pricing.sale.cost"),
          cell: (row) => <OneOf rows={previewOf(row)}>{(found) => <Price schedule={found.cost} />}</OneOf>,
        },
        {
          key: "current",
          width: 160,
          header: t("pricing.sale.current"),
          cell: (row) => <Price schedule={row.current_schedule} />,
        },
        {
          key: "new",
          width: 160,
          header: t("pricing.sale.new"),
          cell: (row) => <NewPrice row={row} />,
        },
        {
          key: "margin",
          width: 110,
          header: t("pricing.sale.margin"),
          cell: (row) => <OneOf rows={previewOf(row)}>{(found) => <Margin row={found} />}</OneOf>,
        },
        { key: "state", width: 170, header: t("pricing.publish.state"), cell: (row) => <Standings row={row} /> },
        {
          key: "act",
          width: 90,
          header: "",
          align: "end",
          cell: (row) =>
            manage && standingOf(row) === "waiting" && row.candidate_id ? (
              <ActionButton size="sm" onClick={() => onPublish(row)}>
                {t("pricing.publish.short")}
              </ActionButton>
            ) : null,
        },
      ]}
    />
  );
}

// What waits, in a line under the title's: how many models have a new price to publish, how many a price below the floor
// to settle. A new price does not apply before it is published.
function Summary({ rows }: { rows: readonly Publication[] }) {
  const { t } = useTranslation();
  const standings = rows.map(standingOf);
  const waiting = standings.filter((standing) => standing === "waiting").length;
  const low = standings.filter((standing) => standing === "floor").length;
  return (
    <Text tone="muted">
      {waiting + low === 0
        ? t("pricing.sale.allCurrent")
        : [
            waiting > 0 ? t("pricing.sale.waiting", { count: waiting }) : null,
            low > 0 ? t("pricing.sale.low", { count: low }) : null,
          ]
            .filter((part) => part !== null)
            .join(t("pricing.sale.join")) + t("pricing.sale.notYet")}
    </Text>
  );
}

// A cost or a margin is of one endpoint. A model that runs on several has one for each, which its drawer lists; here
// they are told apart from none.
function OneOf({ rows, children }: { rows: readonly PreviewRow[]; children: (row: PreviewRow) => ReactNode }) {
  const { t } = useTranslation();
  const [only] = rows;
  if (only === undefined) return "—";
  return rows.length === 1 ? children(only) : t("pricing.sale.byEndpoint", { count: rows.length });
}

// The price the proposal publishes, or that there is none to publish.
function NewPrice({ row }: { row: Publication }) {
  const { t } = useTranslation();
  return standingOf(row) === "current" ? (
    <Text as="span" tone="faint">
      {t("pricing.sale.unchanged")}
    </Text>
  ) : (
    <Price schedule={row.schedule} />
  );
}

// A model's name, which opens its price, and the audience when the price is not for everyone.
function ModelCell({ row, name, onOpen }: { row: Publication; name: string; onOpen: () => void }) {
  const { t } = useTranslation();
  return (
    <Cluster gap={8}>
      <LinkButton label={t("pricing.sale.open", { model: name })} onClick={onOpen}>
        {name}
      </LinkButton>
      {row.audience && row.audience.kind !== "all" ? <Tag>{t(`pricing.audience.${row.audience.kind}`)}</Tag> : null}
    </Cluster>
  );
}
