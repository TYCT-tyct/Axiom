import type { TFunction } from "i18next";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { parseMoney, useFormat } from "../format";
import { Stack } from "./layout";
import s from "./model-pricing.module.css";
import { Table, Td, Th } from "./table";
import { Text } from "./text";

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
      <Th align="end" from="sm">
        {t("publicModels.item.input")}
      </Th>
      <Th align="end" from="sm">
        {t("publicModels.item.output")}
      </Th>
    </>
  );
}

// ModelSection is a part of a model's details under a rule: a title with its note, then the tables it holds. With no
// title the note stands alone, as a line saying why the part is empty.
export function ModelSection({ title, note, children }: { title?: string; note?: string; children?: ReactNode }) {
  return (
    <div className={s.section}>
      <div className={s.head}>
        <Stack gap={4}>
          {title ? (
            <Text as="h3" medium>
              {title}
            </Text>
          ) : null}
          {note ? (
            <Text size={title ? "sm" : "md"} tone="faint">
              {note}
            </Text>
          ) : null}
        </Stack>
      </div>
      {children}
    </div>
  );
}

export function PricingPrice({ pricing, item }: { pricing?: PublicModelPricing; item: "input" | "output" }) {
  const { t } = useTranslation("common");
  const format = useFormat();
  const rate = pricing?.rates?.find((candidate) => candidate.item === item && candidate.headline);
  if (!pricing || !rate) {
    return (
      <Text as="span" tone="faint">
        {t("publicModels.priceUnavailable")}
      </Text>
    );
  }
  return (
    <>
      <Text as="span" nowrap>
        {priceText(format, rate, pricing.currency)}
      </Text>
      <div className={s.per}>{perText(t, rate)}</div>
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
        <caption className={s.caption}>
          <span>{t("publicModels.tierBasis")}</span>{" "}
          <span>{basis.map((item) => label(t, "item", item)).join(" + ")}</span>
        </caption>
      ) : null}
      <thead>
        <tr>
          <Th>{t("publicModels.service")}</Th>
          <Th>{t("publicModels.band")}</Th>
          <Th>{t("publicModels.itemName")}</Th>
          <Th align="end">{t("publicModels.price")}</Th>
        </tr>
      </thead>
      <tbody>
        {rates.map((rate, i) => (
          <tr key={`${rate.service_tier}:${rate.above ?? "base"}:${rate.item}:${i}`}>
            <Td>{label(t, "serviceTier", rate.service_tier)}</Td>
            <Td>
              <span className={s.num}>
                {rate.above === undefined
                  ? t("publicModels.base")
                  : t("publicModels.above", { tokens: format.number(rate.above) })}
              </span>
            </Td>
            <Td>{label(t, "item", rate.item)}</Td>
            <Td align="end">
              {priceText(format, rate, currency)}
              <div className={s.per}>{perText(t, rate)}</div>
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
          <Th align="end">{t("publicModels.perSearch")}</Th>
          <Th align="end">{t("publicModels.perResult")}</Th>
        </tr>
      </thead>
      <tbody>
        {engines.map((engine) => (
          <tr key={engine.name}>
            <Td>
              {engine.name}
              {engine.default ? <div className={s.per}>{t("publicModels.defaultEngine")}</div> : null}
            </Td>
            <Td align="end">
              {engine.price_per_search === "0"
                ? t("publicModels.modelWebSearchRate")
                : `${format.decimal(engine.price_per_search)} ${currency}`}
            </Td>
            <Td align="end">
              {format.decimal(engine.price_per_extra_result)} {currency}
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}
