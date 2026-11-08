import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { Button, EmptyState, Icon, Panel, Table, Td, Th, useFormat } from "@axiom/ui";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Time } from "../parts";
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
    <Panel
      title={t("models.prices.title")}
      action={
        manage ? (
          <div className="flex flex-wrap gap-2">
            {byHand(model) ? (
              <Button size="sm" onClick={() => setDialog({ kind: "publish" })}>
                <Icon name="plus" />
                {t("models.prices.publish")}
              </Button>
            ) : null}
            <Button size="sm" onClick={() => setDialog({ kind: "source" })}>
              {t("models.prices.changeSource")}
            </Button>
          </div>
        ) : undefined
      }
    >
      {model.prices.length === 0 ? (
        <EmptyState compact title={t("models.prices.empty.title")}>
          {byHand(model) ? t("models.prices.empty.body") : t("models.prices.empty.synced")}
        </EmptyState>
      ) : (
        <div className="divide-y divide-b1">
          {model.prices.map((price) => (
            <PriceCard
              key={price.id}
              price={price}
              onPublish={
                manage && byHand(model) ? () => setDialog({ kind: "publish", currency: price.currency }) : undefined
              }
            />
          ))}
        </div>
      )}
      {dialog?.kind === "publish" ? (
        <PublishPriceDialog model={model} currency={dialog.currency} onClose={() => setDialog(null)} />
      ) : null}
      {dialog?.kind === "source" ? <SourceDialog model={model} onClose={() => setDialog(null)} /> : null}
    </Panel>
  );
}

// One currency's current price: where it came from and when, and the price of each item it prices.
function PriceCard({ price, onPublish }: { price: Price; onPublish: (() => void) | undefined }) {
  const { t } = useTranslation();
  const { schedule } = price;
  return (
    <section className="space-y-3 p-4" aria-label={price.currency}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-t1">
          {price.currency}
          {" · "}
          {t(`models.source.${price.source}`)}
          <span className="ml-2 text-label text-t3">
            <Time at={price.published_at} />
          </span>
        </p>
        {onPublish ? (
          <Button size="sm" onClick={onPublish}>
            {t("models.prices.newVersion")}
          </Button>
        ) : null}
      </div>
      {schedule ? <Rates schedule={schedule} /> : <p className="text-t3">{t("models.prices.withdrawn")}</p>}
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
    <div className="space-y-4">
      <RateTable title={t("models.prices.rates", { currency: schedule.currency })} rates={schedule.rates} />
      {schedule.context_tiers?.tiers.map((band) => (
        <RateTable key={`band:${band.above}`} title={above(band.above)} rates={band.rates} />
      ))}
      {tiers.map(([tier, prices]) => {
        const name = t(`models.tier.${tier as ServiceTier}`);
        return (
          <div key={tier} className="space-y-4">
            <RateTable title={t("models.prices.serviceTier", { tier: name })} rates={prices.rates} />
            {prices.context_tiers?.map((band) => (
              <RateTable
                key={`${tier}:${band.above}`}
                title={t("models.prices.serviceTierBand", { tier: name, band: above(band.above) })}
                rates={band.rates}
              />
            ))}
          </div>
        );
      })}
    </div>
  );
}

// RateTable is one set of prices under its heading, item by item in the catalog's order.
function RateTable({ title, rates }: { title: string; rates: RateList }) {
  const { t } = useTranslation();
  const { decimal } = useFormat();
  const priced = rateItems.filter((item) => rates[item] !== undefined);
  return (
    <div className="space-y-1">
      <h4 className="text-label text-t2">{title}</h4>
      {priced.length === 0 ? (
        <p className="text-label text-t3">{t("models.prices.noRates")}</p>
      ) : (
        <Table label={title}>
          <thead>
            <tr>
              <Th>{t("models.prices.item")}</Th>
              <Th className="text-right">{t("models.prices.price")}</Th>
            </tr>
          </thead>
          <tbody>
            {priced.map((item) => (
              <tr key={item}>
                <Td>{t(`models.item.${item}`)}</Td>
                <Td className="text-right tabular-nums">
                  {decimal(showRate(item, rates[item] ?? ""))}
                  <span className="ml-1 text-label text-t4">
                    {t(`models.prices.unit.${isPerUse(item) ? "use" : "million"}`)}
                  </span>
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </div>
  );
}
