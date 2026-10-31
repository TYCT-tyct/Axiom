import type { TFunction } from "i18next";
import { type ReactNode, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { useFormat } from "../format";
import { Button } from "./button";
import { SelectField, TextField } from "./form";
import { Icon } from "./icon";
import { Badge, Table, Td, Th } from "./panel";

// The public models and prices page: what a visitor may see of the catalog, at the price of the audience that asked.
// Every member but the identity is optional, so a page served by an older node shows what it has and marks the rest
// unavailable instead of failing (docs/frontend.md「滚动发布」).
type PublicModelRate = {
  service_tier: string;
  above?: number; // absent is the base band; 0 is an explicit first band above 0
  item: string;
  unit: string;
  amount: string;
};

type PublicModelPricing = {
  currency: string;
  basis?: string[];
  rates?: PublicModelRate[];
};

export type PublicModel = {
  id: string;
  name: string;
  author: string;
  context_length?: number;
  max_output?: number;
  streams?: boolean;
  abilities?: string[];
  pricing?: PublicModelPricing;
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

const label = (t: TFunction<"common">, group: string, name: string) =>
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

function Price({ model, item }: { model: PublicModel; item: "input" | "output" }) {
  const { t } = useTranslation("common");
  const format = useFormat();
  const rate = model.pricing?.rates ? baseRate(model.pricing.rates, item) : undefined;
  if (!model.pricing || !rate) {
    return <span className="text-t3">{t("publicModels.priceUnavailable")}</span>;
  }
  return (
    <>
      <span className="whitespace-nowrap">{priceText(format, rate, model.pricing.currency)}</span>
      {rate.unit === "token" ? <span className="block text-label text-t3">{t("publicModels.perMillion")}</span> : null}
    </>
  );
}

export const modelAbilities = [
  "tools",
  "images",
  "documents",
  "reasoning",
  "prompt_cache",
  "structured_output",
  "web_search",
  "code_execution",
] as const;

type ModelAbility = (typeof modelAbilities)[number];
export type ModelFilters = { q?: string; author?: string; ability?: ModelAbility };

// modelFiltersOf reads the three filters both pages share. An ability outside the contract's list is no filter, not a
// request the API would refuse.
function modelFiltersOf(form: HTMLFormElement): ModelFilters {
  const data = new FormData(form);
  const text = (name: string) => String(data.get(name) ?? "").trim() || undefined;
  const ability = text("ability") as ModelAbility | undefined;
  return {
    q: text("q"),
    author: text("author"),
    ability: ability && modelAbilities.includes(ability) ? ability : undefined,
  };
}

// ModelFilterForm is the website's and the console's filter row: the fields, the apply button and the caller's reset,
// so the two pages cannot drift apart. The caller owns what a submitted filter means (a URL push or a router navigate).
export function ModelFilterForm({
  filters,
  onApply,
  reset,
}: {
  filters: ModelFilters;
  onApply: (filters: ModelFilters) => void;
  reset: ReactNode;
}) {
  const { t } = useTranslation("common");
  return (
    <form
      key={JSON.stringify(filters)}
      noValidate
      className="grid gap-4 sm:grid-cols-2 lg:grid-cols-[repeat(3,minmax(0,1fr))_auto] lg:items-end"
      onSubmit={(event) => {
        event.preventDefault();
        onApply(modelFiltersOf(event.currentTarget));
      }}
    >
      <TextField
        label={t("publicModels.search")}
        name="q"
        maxLength={100}
        defaultValue={filters.q}
        placeholder={t("publicModels.searchHint")}
        autoComplete="off"
        spellCheck={false}
      />
      <TextField
        label={t("publicModels.author")}
        name="author"
        maxLength={100}
        defaultValue={filters.author}
        autoComplete="off"
        spellCheck={false}
      />
      <SelectField label={t("publicModels.ability")} name="ability" defaultValue={filters.ability ?? ""}>
        <option value="">{t("publicModels.allAbilities")}</option>
        {modelAbilities.map((ability) => (
          <option key={ability} value={ability}>
            {t(`publicModels.abilityNames.${ability}`)}
          </option>
        ))}
      </SelectField>
      <div className="flex gap-2">
        <Button variant="primary" size="md" type="submit">
          {t("publicModels.apply")}
        </Button>
        {reset}
      </div>
    </form>
  );
}

// ModelCatalog is the list and the details of the selected model. It is presentational: the page owns the URL
// filters and the request, and both the website and the console use this component so the two read the same.
export function ModelCatalog({ models, note }: { models: PublicModel[]; note?: ReactNode }) {
  const { t } = useTranslation("common");
  const [open, setOpen] = useState<string | null>(null);
  return (
    <div className="space-y-4">
      <Table label={t("publicModels.title")}>
        <thead>
          <tr>
            <Th>{t("publicModels.search")}</Th>
            <Th className="hidden md:table-cell">{t("publicModels.author")}</Th>
            <Th className="hidden text-right lg:table-cell">{t("publicModels.context")}</Th>
            <Th className="hidden text-right xl:table-cell">{t("publicModels.maxOutput")}</Th>
            <Th className="hidden 2xl:table-cell">{t("publicModels.ability")}</Th>
            <Th className="hidden text-right sm:table-cell">{t("publicModels.item.input")}</Th>
            <Th className="hidden text-right sm:table-cell">{t("publicModels.item.output")}</Th>
            <Th />
          </tr>
        </thead>
        <tbody>
          {models.map((model) => (
            <ModelRow
              key={model.id}
              model={model}
              open={open === model.id}
              onToggle={() => setOpen(open === model.id ? null : model.id)}
            />
          ))}
        </tbody>
      </Table>
      {note ? <p className="max-w-prose text-ui text-t3">{note}</p> : null}
    </div>
  );
}

// ModelRow is a model and, when it is open, its details in the row right under it, where the person who opened them is
// looking; a list of a hundred models would otherwise show them far below the button.
function ModelRow({ model, open, onToggle }: { model: PublicModel; open: boolean; onToggle: () => void }) {
  const details = useId();
  return (
    <>
      <ModelSummary model={model} open={open} details={details} onToggle={onToggle} />
      {open ? (
        <tr>
          <td colSpan={8} className="border-b border-b1 bg-s2">
            <ModelDetails id={details} model={model} />
          </td>
        </tr>
      ) : null}
    </>
  );
}

function ModelSummary({
  model,
  open,
  details,
  onToggle,
}: {
  model: PublicModel;
  open: boolean;
  details: string;
  onToggle: () => void;
}) {
  const { t } = useTranslation("common");
  const format = useFormat();
  return (
    <tr className="hover:bg-s2">
      <Td className="max-w-44 sm:max-w-64">
        <p className="truncate text-t1" title={model.name}>
          {model.name}
        </p>
        <p className="mt-0.5 truncate font-mono text-label text-t3" title={model.id}>
          {model.id}
        </p>
        {/* Below sm the price columns are hidden, so the input price reads under the name and Details stays in view. */}
        <div className="mt-1 text-label tabular-nums sm:hidden">
          <span className="text-t3">{t("publicModels.item.input")} </span>
          <Price model={model} item="input" />
        </div>
      </Td>
      <Td className="hidden max-w-40 md:table-cell">
        <p className="truncate" title={model.author}>
          {model.author}
        </p>
      </Td>
      <Td className="hidden text-right whitespace-nowrap tabular-nums lg:table-cell">
        {model.context_length ? format.number(model.context_length) : "—"}
      </Td>
      <Td className="hidden text-right whitespace-nowrap tabular-nums xl:table-cell">
        {model.max_output ? format.number(model.max_output) : "—"}
      </Td>
      <Td className="hidden 2xl:table-cell">
        <span className="flex max-w-64 flex-wrap gap-1">
          {(model.abilities ?? []).map((ability) => (
            <Badge key={ability}>{label(t, "abilityNames", ability)}</Badge>
          ))}
        </span>
      </Td>
      <Td className="hidden text-right tabular-nums sm:table-cell">
        <Price model={model} item="input" />
      </Td>
      <Td className="hidden text-right tabular-nums sm:table-cell">
        <Price model={model} item="output" />
      </Td>
      <Td className="text-right">
        <Button size="sm" variant="ghost" aria-expanded={open} aria-controls={details} onClick={onToggle}>
          {t(open ? "publicModels.hide" : "publicModels.details")}
          <Icon name={open ? "down" : "right"} />
        </Button>
      </Td>
    </tr>
  );
}

function ModelDetails({ id, model }: { id: string; model: PublicModel }) {
  const { t } = useTranslation("common");
  const pricing = model.pricing;
  const rates = pricing?.rates ?? [];
  return (
    // w-0 min-w-full: the details take the table's width without widening it on a narrow screen.
    <section id={id} aria-label={model.name} className="w-0 min-w-full pb-1">
      <ModelFacts model={model} />
      <div className="mt-4 border-t border-b1">
        {!pricing ? (
          <p className="px-4 py-3 text-t3">{t("publicModels.priceUnavailable")}</p>
        ) : rates.length === 0 ? (
          <p className="px-4 py-3 text-t3">{t("publicModels.noRates")}</p>
        ) : (
          <RateTable model={model} currency={pricing.currency} rates={rates} />
        )}
      </div>
    </section>
  );
}

function ModelFacts({ model }: { model: PublicModel }) {
  const { t } = useTranslation("common");
  const format = useFormat();
  return (
    <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-6 gap-y-2 px-4 pt-4 text-ui">
      <dt className="text-t3">{t("publicModels.author")}</dt>
      <dd className="[overflow-wrap:anywhere]">{model.author}</dd>
      <dt className="text-t3">{t("publicModels.context")}</dt>
      <dd className="tabular-nums">{model.context_length ? format.number(model.context_length) : "—"}</dd>
      <dt className="text-t3">{t("publicModels.maxOutput")}</dt>
      <dd className="tabular-nums">{model.max_output ? format.number(model.max_output) : "—"}</dd>
      <dt className="text-t3">{t("publicModels.streams")}</dt>
      <dd>{model.streams === undefined ? "—" : t(model.streams ? "publicModels.yes" : "publicModels.no")}</dd>
      <dt className="text-t3">{t("publicModels.ability")}</dt>
      <dd className="flex flex-wrap gap-1">
        {(model.abilities ?? []).length === 0
          ? "—"
          : (model.abilities ?? []).map((ability) => <Badge key={ability}>{label(t, "abilityNames", ability)}</Badge>)}
      </dd>
    </dl>
  );
}

function RateTable({ model, currency, rates }: { model: PublicModel; currency: string; rates: PublicModelRate[] }) {
  const { t } = useTranslation("common");
  const format = useFormat();
  return (
    <Table label={`${model.name} ${t("publicModels.title")}`}>
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
