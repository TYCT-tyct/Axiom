import type { AdminSchemas } from "@axiom/api";
import { DataTable, LinkButton, Tooltip } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import type { Lines } from "../lines";
import { Code } from "../parts";
import { ModelState } from "./parts";

type Summary = AdminSchemas["ModelSummary"];

// The lines a model is on, as the catalog shows them: how many are in use, and how many it has when some are stopped.
// An endpoint is in use when it is enabled and its line can take a request.
function lineCounts(lines: Lines, modelId: string) {
  const endpoints = lines.endpointsOf(modelId);
  return {
    inUse: endpoints.filter(({ endpoint, line }) => endpoint.enabled && line?.usable).length,
    all: endpoints.length,
    names: endpoints.map(({ endpoint, line }) => ({ name: line?.name ?? endpoint.group_id, on: endpoint.enabled })),
  };
}

function ModelLines({ lines, modelId }: { lines: Lines; modelId: string }) {
  const { t } = useTranslation();
  if (!lines.ready) {
    return null;
  }
  const { inUse, all, names } = lineCounts(lines, modelId);
  if (all === 0) {
    return <span>{t("models.lines.none")}</span>;
  }
  const list = names.map(({ name, on }) => `${name}${on ? "" : t("models.lines.stopped")}`).join(", ");
  return (
    <>
      <Tooltip text={list}>{t("models.lines.inUse", { count: inUse })}</Tooltip>
      {all > inUse ? <span>{` ${t("models.lines.total", { count: all })}`}</span> : null}
    </>
  );
}

// A column that is there only when its data is.
// A column of words, at a width.
const text = (key: string, width: number, header: string, cell: (row: Summary) => string) => ({
  key,
  width,
  header,
  cell,
});

const when = <Column,>(on: boolean, column: Column): Column[] => (on ? [column] : []);

// CatalogTable lists the models a page has read: each opens its drawer from its name, and the lines it runs on show at
// the end where the session may read the suppliers.
export function CatalogTable({
  rows,
  authors,
  lines,
  onOpen,
}: {
  rows: readonly Summary[];
  authors: ReadonlyMap<string, string>;
  lines: Lines;
  onOpen: (id: string) => void;
}) {
  const { t } = useTranslation();
  const named = rows.some((row) => row.family !== undefined);
  const billed = rows.some((row) => row.billing_units?.length);
  return (
    <DataTable
      fixed
      label={t("nav.models")}
      rows={rows}
      rowKey={(row) => row.id}
      columns={[
        {
          key: "slug",
          width: 260,
          header: t("models.col.slug"),
          cell: (row) => (
            <LinkButton label={t("models.open", { slug: row.slug })} onClick={() => onOpen(row.id)}>
              <Code>{row.slug}</Code>
            </LinkButton>
          ),
        },
        text("author", 140, t("models.col.author"), (row) => authors.get(row.author) ?? row.author),
        ...when(named, {
          key: "family",
          width: 140,
          header: t("models.col.family"),
          cell: (row: Summary) => (row.family ? <Code>{row.family}</Code> : "—"),
        }),
        {
          key: "status",
          width: 110,
          header: t("models.col.status"),
          cell: (row) => <ModelState status={row.status} />,
        },
        ...when(billed, {
          key: "units",
          width: 130,
          header: t("models.col.units"),
          muted: true,
          cell: (row: Summary) => row.billing_units?.map((unit) => t(`models.unit.${unit}`)).join(", ") || "—",
        }),
        text("basis", 120, t("models.col.basis"), (row) => t(`models.basis.${row.version_basis}`)),
        text("source", 140, t("models.col.source"), (row) => t(`models.source.${row.price_source}`)),
        ...when(lines.allowed, {
          key: "lines",
          width: 150,
          header: t("models.col.lines"),
          cell: (row: Summary) => <ModelLines lines={lines} modelId={row.id} />,
        }),
      ]}
    />
  );
}
