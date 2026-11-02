import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import { useFormat } from "../format";
import { Table, Td, Th } from "./panel";

type PublicModelRate = {
  service_tier: string;
  above?: number; // absent is the base band; 0 is an explicit first band above 0
  item: string;
  unit: string;
  amount: string;
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

// perMillionBillionths turns a price per token into billionths of a currency unit per million tokens, without a
// floating point number: a UnitPrice has at most 15 decimals, and moving the point six places leaves at most nine.
export function perMillionBillionths(amount: string): string | null {
  const [whole = "0", fraction = ""] = amount.split(".");
  if (fraction.length > 15) {
    return null;
  }
  const digits = (whole + fraction).replace(/^0+(?=\d)/, "");
  return (digits + "0".repeat(15 - fraction.length)).replace(/^0+(?=\d)/, "");
}

export const label = (t: TFunction<"common">, group: string, name: string) =>
  (t as unknown as (key: string, options?: { defaultValue?: string }) => string)(`publicModels.${group}.${name}`, {
    defaultValue: name,
  });

function priceText(format: ReturnType<typeof useFormat>, rate: PublicModelRate, currency: string): string {
  if (rate.unit === "token") {
    const perMillion = perMillionBillionths(rate.amount);
    if (perMillion !== null) {
      return format.money({ amount: perMillion, currency });
    }
  }
  return `${format.decimal(rate.amount)} ${currency}`;
}

// baseRate is the headline price: the standard tier's price in the base band. An explicit above: 0 tier is a different
// band even though its threshold is zero, so it must not stand in for the base price.
export function baseRate<T extends Pick<PublicModelRate, "item" | "above" | "service_tier">>(
  rates: T[],
  item: string,
): T | undefined {
  return (
    rates.find(
      (candidate) => candidate.item === item && candidate.above === undefined && candidate.service_tier === "standard",
    ) ?? rates.find((candidate) => candidate.item === item && candidate.above === undefined)
  );
}

export function PricingPrice({ pricing, item }: { pricing?: PublicModelPricing; item: "input" | "output" }) {
  const { t } = useTranslation("common");
  const format = useFormat();
  const rate = pricing?.rates ? baseRate(pricing.rates, item) : undefined;
  if (!pricing || !rate) {
    return <span className="text-t3">{t("publicModels.priceUnavailable")}</span>;
  }
  return (
    <>
      <span className="whitespace-nowrap">{priceText(format, rate, pricing.currency)}</span>
      {rate.unit === "token" ? <span className="block text-label text-t3">{t("publicModels.perMillion")}</span> : null}
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
              <span className="block text-label text-t3">
                {rate.unit === "token" ? t("publicModels.perMillion") : `/ ${label(t, "unit", rate.unit)}`}
              </span>
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
