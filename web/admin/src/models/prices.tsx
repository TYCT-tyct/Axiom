import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { ActionButton, Card, Cluster, EmptyState, Stack, Table, Td, Text, useFormat } from "@axiom/ui";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Head, Time } from "../parts";
import { isPerUse, rateItems, serviceTiers } from "./catalog";
import { PublishPriceDialog, SourceDialog } from "./price-dialogs";
import { showRate } from "./price-fields";

type Model = AdminSchemas["Model"];
type Price = AdminSchemas["ReferencePrice"];

// Only a hand-kept source can be published to by a person; a model priced from a synced source follows that source.
function byHand(model: Model) {
  return model.price_source === "official" || model.price_source === "manual";
}

export function Prices({ model }: { model: Model }) {
  const { t } = useTranslation();
  const manage = can(useSession(), "catalog.manage") && model.status !== "retired";
  const [dialog, setDialog] = useState<{ kind: "publish"; currency?: string } | { kind: "source" } | null>(null);
  return (
    <Card
      title={t("models.prices.title")}
      actions={
        manage ? (
          <Cluster>
            {byHand(model) ? (
              <ActionButton size="sm" icon="plus" onClick={() => setDialog({ kind: "publish" })}>
                {t("models.prices.publish")}
              </ActionButton>
            ) : null}
            <ActionButton size="sm" onClick={() => setDialog({ kind: "source" })}>
              {t("models.prices.changeSource")}
            </ActionButton>
          </Cluster>
        ) : undefined
      }
    >
      {model.prices.length === 0 ? (
        <EmptyState compact title={t("models.prices.empty.title")}>
          {byHand(model) ? t("models.prices.empty.body") : t("models.prices.empty.synced")}
        </EmptyState>
      ) : (
        <Stack gap={24}>
          {model.prices.map((price) => (
            <PriceCard
              key={price.id}
              price={price}
              onPublish={
                manage && byHand(model) ? () => setDialog({ kind: "publish", currency: price.currency }) : undefined
              }
            />
          ))}
        </Stack>
      )}
      {dialog?.kind === "publish" ? (
        <PublishPriceDialog model={model} currency={dialog.currency} onClose={() => setDialog(null)} />
      ) : null}
      {dialog?.kind === "source" ? <SourceDialog model={model} onClose={() => setDialog(null)} /> : null}
    </Card>
  );
}

// One currency's current price: where it came from and when, and the price of each item it prices.
function PriceCard({ price, onPublish }: { price: Price; onPublish: (() => void) | undefined }) {
  const { t } = useTranslation();
  const { schedule } = price;
  return (
    <section aria-label={price.currency}>
      <Stack gap={12}>
        <Cluster justify="between">
          <Text>
            {price.currency}
            {" · "}
            {t(`models.source.${price.source}`)}{" "}
            <Text as="span" size="sm" tone="faint">
              <Time at={price.published_at} />
            </Text>
          </Text>
          {onPublish ? (
            <ActionButton size="sm" onClick={onPublish}>
              {t("models.prices.newVersion")}
            </ActionButton>
          ) : null}
        </Cluster>
        {schedule ? <Rates schedule={schedule} /> : <Text tone="faint">{t("models.prices.withdrawn")}</Text>}
      </Stack>
    </section>
  );
}

type Schedule = AdminSchemas["PriceSchedule"];
type RateList = AdminSchemas["Rates"];
type ServiceTier = (typeof serviceTiers)[number];

// Rates is a whole price list as the catalog stores it: the standard prices, each context band by its threshold, and
// each service tier with its own bands, every price per unit as the catalog shows it. A band or tier is shown with the
// prices it lists, as stored.
export function Rates({ schedule }: { schedule: Schedule }) {
  const { t } = useTranslation();
  const { number } = useFormat();
  const basis = (schedule.context_tiers?.basis ?? []).map((item) => t(`models.item.${item}`)).join(" + ");
  const above = (tokens: number) => t("models.prices.above", { above: number(tokens), basis });
  const tiers = Object.entries(schedule.service_tiers ?? {}).sort(
    ([a], [b]) => serviceTiers.indexOf(a as ServiceTier) - serviceTiers.indexOf(b as ServiceTier),
  );
  return (
    <Stack>
      <RateTable title={t("models.prices.rates", { currency: schedule.currency })} rates={schedule.rates} />
      {schedule.context_tiers?.tiers.map((band) => (
        <RateTable key={`band:${band.above}`} title={above(band.above)} rates={band.rates} />
      ))}
      {tiers.map(([tier, prices]) => {
        const name = t(`models.tier.${tier as ServiceTier}`);
        return (
          <Stack key={tier}>
            <RateTable title={t("models.prices.serviceTier", { tier: name })} rates={prices.rates} />
            {prices.context_tiers?.map((band) => (
              <RateTable
                key={`${tier}:${band.above}`}
                title={t("models.prices.serviceTierBand", { tier: name, band: above(band.above) })}
                rates={band.rates}
              />
            ))}
          </Stack>
        );
      })}
    </Stack>
  );
}

// RateTable is one set of prices under its heading, item by item in the catalog's order.
function RateTable({ title, rates }: { title: string; rates: RateList }) {
  const { t } = useTranslation();
  const { decimal } = useFormat();
  const priced = rateItems.filter((item) => rates[item] !== undefined);
  return (
    <Stack gap={4}>
      <Text as="h3" size="sm" tone="muted">
        {title}
      </Text>
      {priced.length === 0 ? (
        <Text size="sm" tone="faint">
          {t("models.prices.noRates")}
        </Text>
      ) : (
        <Table label={title}>
          <Head
            cols={[
              [t("models.prices.item"), { main: true }],
              [t("models.prices.price"), { align: "end" }],
            ]}
          />
          <tbody>
            {priced.map((item) => (
              <tr key={item}>
                <Td>{t(`models.item.${item}`)}</Td>
                <Td align="end">
                  <Cluster gap={4} justify="end">
                    <span>{decimal(showRate(item, rates[item] ?? ""))}</span>
                    <Text as="span" size="sm" tone="faint">
                      {t(`models.prices.unit.${isPerUse(item) ? "use" : "million"}`)}
                    </Text>
                  </Cluster>
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </Stack>
  );
}
