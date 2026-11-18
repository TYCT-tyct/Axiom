import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import { parseMoney, useFormat } from "../format";
import { Table, Td, Th } from "./table";

type PublicModelRate = {
  service_tier: string;
  above?: number; // absent is the base band; 0 is an explicit first band above 0
  item: string;
  unit: string;
  amount: string;
  // The server names the rate a list shows for each item and the amount to show; an older node sends neither (#695).
  headline?: boolean;
  display_amount?: string;
  display_per?: number;
};

export type PublicSearchEnginePrice = {
  name: string;
  price_per_search: string;
  price_per_extra_result: string;
  default: boolean;
};

export type PublicModelPricing = {
  currency: string;
  basis?: string[];
  rates?: PublicModelRate[];
  search_engines?: PublicSearchEnginePrice[];
};

export const label = (t: TFunction<"common">, group: string, name: string) =>
  (t as unknown as (key: string, options?: { defaultValue?: string }) => string)(`publicModels.${group}.${name}`, {
    defaultValue: name,
  });

const perMillion = 1_000_000;

// priceText shows the amount the server says to show: per million tokens as money, any other unit as an exact decimal.
// An older node sends no display amount, so the price per unit is shown as it came instead of being converted here.
function priceText(format: ReturnType<typeof useFormat>, rate: PublicModelRate, currency: string): string {
  if (rate.display_amount !== undefined && rate.display_per === perMillion) {
    const amount = parseMoney(rate.display_amount);
    if (amount !== null) {
      return format.money({ amount, currency });
    }
  }
  return `${format.decimal(rate.display_amount ?? rate.amount)} ${currency}`;
}

const perText = (t: TFunction<"common">, rate: PublicModelRate) =>
  rate.display_per === perMillion ? t("publicModels.perMillion") : `/ ${label(t, "unit", rate.unit)}`;

// PriceHeads are the input and output price column heads the model list and the endpoint table share; they render
// nothing when the site keeps its prices to signed-in members, so the columns go with them.
export function PriceHeads({ show }: { show: boolean }) {
  const { t } = useTranslation("common");
  if (!show) {
    return null;
  }
  return (
    <>
      <Th className="hidden text-right sm:table-cell">{t("publicModels.item.input")}</Th>
      <Th className="hidden text-right sm:table-cell">{t("publicModels.item.output")}</Th>
    </>
  );
}

export function PricingPrice({ pricing, item }: { pricing?: PublicModelPricing; item: "input" | "output" }) {
  const { t } = useTranslation("common");
  const format = useFormat();
  const rate = pricing?.rates?.find((candidate) => candidate.item === item && candidate.headline);
  if (!pricing || !rate) {
    return <span className="text-t3">{t("publicModels.priceUnavailable")}</span>;
  }
  return (
    <>
      <span className="whitespace-nowrap">{priceText(format, rate, pricing.currency)}</span>
      <span className="block text-label text-t3">{perText(t, rate)}</span>
    </>
  );
}

export function RateTable({
  title,
  currency,
  basis,
  rates,
}: {
  title: string;
  currency: string;
  basis?: string[];
  rates: PublicModelRate[];
}) {
  const { t } = useTranslation("common");
  const format = useFormat();
  return (
    <Table label={title}>
      {basis?.length ? (
        <caption className="px-4 py-2 text-left text-label text-t3">
          <span>{t("publicModels.tierBasis")}</span>
          <span className="ml-1">{basis.map((item) => label(t, "item", item)).join(" + ")}</span>
        </caption>
      ) : null}
      <thead>
        <tr>
          <Th>{t("publicModels.service")}</Th>
          <Th>{t("publicModels.band")}</Th>
          <Th>{t("publicModels.itemName")}</Th>
          <Th className="text-right">{t("publicModels.price")}</Th>
        </tr>
      </thead>
      <tbody>
        {rates.map((rate, i) => (
          <tr key={`${rate.service_tier}:${rate.above ?? "base"}:${rate.item}:${i}`}>
            <Td>{label(t, "serviceTier", rate.service_tier)}</Td>
            <Td className="whitespace-nowrap tabular-nums">
              {rate.above === undefined
                ? t("publicModels.base")
                : t("publicModels.above", { tokens: format.number(rate.above) })}
            </Td>
            <Td>{label(t, "item", rate.item)}</Td>
            <Td className="text-right whitespace-nowrap tabular-nums">
              {priceText(format, rate, currency)}
              <span className="block text-label text-t3">{perText(t, rate)}</span>
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

// SearchEnginePrices is what one web search through each enabled backend costs: per search plus each result after
// the first. A zero search price is the model's web_search rate, which the rate table already shows.
export function SearchEnginePrices({
  title,
  currency,
  engines,
}: {
  title: string;
  currency: string;
  engines: PublicSearchEnginePrice[];
}) {
  const { t } = useTranslation("common");
  const format = useFormat();
  return (
    <Table label={title}>
      <thead>
        <tr>
          <Th>{t("publicModels.engine")}</Th>
          <Th className="text-right">{t("publicModels.perSearch")}</Th>
          <Th className="text-right">{t("publicModels.perResult")}</Th>
        </tr>
      </thead>
      <tbody>
        {engines.map((engine) => (
          <tr key={engine.name}>
            <Td>
              {engine.name}
              {engine.default ? (
                <span className="block text-label text-t3">{t("publicModels.defaultEngine")}</span>
              ) : null}
            </Td>
            <Td className="text-right whitespace-nowrap tabular-nums">
              {engine.price_per_search === "0"
                ? t("publicModels.modelWebSearchRate")
                : `${format.decimal(engine.price_per_search)} ${currency}`}
            </Td>
            <Td className="text-right whitespace-nowrap tabular-nums">
              {format.decimal(engine.price_per_extra_result)} {currency}
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}
