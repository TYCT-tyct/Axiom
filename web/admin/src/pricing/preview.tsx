import { type AdminSchemas, adminApi, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { DataState, EmptyState, Table, Td, Th, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { billingItems, percent } from "../models/catalog";
import { showRate } from "../models/price-fields";
import { Code, State } from "../parts";

export type PreviewRequest = AdminSchemas["PricingPreviewRequest"];
type Row = AdminSchemas["PricingPreviewRow"];

// A preview asks what models would sell for, and with what margin, under the stored rules, with a rule added or put in
// place of one for this preview only. It stores nothing, so it is asked as a query: the same request is the same answer
// until a change invalidates it.
function usePreview(request: PreviewRequest | null) {
  return useQuery({
    queryKey: [accountKey, "preview", request],
    queryFn: ({ signal }) =>
      unwrap(adminApi.POST("/api/admin/v1/pricing/previews", { body: request as PreviewRequest, signal })),
    enabled: request !== null,
    staleTime: 0,
  });
}

const tones = {
  priced: "good",
  cost_unknown: "warn",
  below_floor: "warn",
  unpriced: "bad",
} as const satisfies Record<Row["status"], "good" | "warn" | "bad">;

// The two prices people compare first, input and output, as they read them: per million tokens.
function Price({ schedule }: { schedule: AdminSchemas["PriceSchedule"] | undefined }) {
  const { t } = useTranslation();
  const { decimal } = useFormat();
  if (!schedule) {
    return <>{"—"}</>;
  }
  const shown = billingItems.filter((item) => item === "input" || item === "output");
  return (
    <span className="whitespace-nowrap tabular-nums">
      {shown.map((item) => {
        const price = schedule.rates[item];
        return (
          <span key={item} className="mr-3 inline-block">
            <span className="text-label text-t4">{t(`models.item.${item}`)}</span>{" "}
            {price === undefined ? "—" : decimal(showRate(item, price))}
          </span>
        );
      })}
      <span className="text-label text-t4">{schedule.currency}</span>
    </span>
  );
}

// The lowest margin of the sale over the cost, as a percentage; a loss shows as one.
function Margin({ row }: { row: Row }) {
  const { t } = useTranslation();
  const { decimal } = useFormat();
  if (row.margin_ratio === undefined) {
    return <>{"—"}</>;
  }
  const loss = row.margin_ratio.startsWith("-");
  return (
    <State tone={loss ? "bad" : "good"}>
      {t("pricing.preview.margin", { percent: decimal(percent(row.margin_ratio)) })}
    </State>
  );
}

function PreviewTable({ rows }: { rows: Row[] }) {
  const { t } = useTranslation();
  return (
    <Table label={t("pricing.preview.title")}>
      <thead>
        <tr>
          <Th>{t("pricing.preview.model")}</Th>
          <Th>{t("pricing.preview.status")}</Th>
          <Th>{t("pricing.preview.sale")}</Th>
          <Th className="hidden md:table-cell">{t("pricing.preview.cost")}</Th>
          <Th>{t("pricing.preview.marginTitle")}</Th>
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => (
          <tr key={`${row.model_id}/${row.endpoint_id ?? ""}`}>
            <Td className="max-w-56 min-w-32">
              <p className="truncate text-t1" title={row.slug}>
                <Code>{row.slug}</Code>
              </p>
              {row.endpoint_id ? <p className="text-label text-t4">{row.endpoint_id.slice(0, 8)}</p> : null}
            </Td>
            <Td>
              <State tone={tones[row.status]}>{t(`pricing.preview.state.${row.status}`)}</State>
            </Td>
            <Td>
              <Price schedule={row.sale} />
            </Td>
            <Td className="hidden md:table-cell">
              <Price schedule={row.cost} />
            </Td>
            <Td>
              <Margin row={row} />
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

// PreviewResult shows a preview's rows in the four states of a view of data.
export function PreviewResult({ request }: { request: PreviewRequest | null }) {
  const { t } = useTranslation();
  const preview = usePreview(request);
  if (request === null) {
    return <EmptyState compact title={t("pricing.preview.idle")} />;
  }
  return (
    <DataState
      query={preview}
      compact
      isEmpty={(data) => data.data.length === 0}
      empty={<EmptyState compact title={t("pricing.preview.none")} />}
    >
      {(data) => <PreviewTable rows={data.data} />}
    </DataState>
  );
}
