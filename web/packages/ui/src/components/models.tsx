import { type ReactNode, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { useFormat } from "../format";
import { Button } from "./button";
import { SelectField, TextField } from "./form";
import { Icon } from "./icon";
import { EndpointSection } from "./model-endpoints";
import {
  label,
  PriceHeads,
  PricingPrice,
  type PublicModelPricing,
  RateTable,
  SearchEnginePrices,
} from "./model-pricing";
import { Badge, Table, Td, Th } from "./panel";

// The public models and prices page: what a visitor may see of the catalog, at the price of the audience that asked.
// Every member but the identity is optional, so a page served by an older node shows what it has and marks the rest
// unavailable instead of failing (docs/frontend.md「滚动发布」). `pricing` is absent on every model and endpoint when
// the site keeps its prices to signed-in members; that is a no-price state, not an unavailable one.
export type PublicModel = {
  id: string;
  name: string;
  author: string;
  context_length?: number;
  max_output?: number;
  streams?: boolean;
  abilities?: string[];
  pricing?: PublicModelPricing;
  endpoints?: PublicEndpoint[];
};

// PublicEndpoint is one group that serves a model: the codename a provider filter or preset names, the attributes it
// serves and its own price. Missing endpoints mean an older node, not a model without endpoints.
export type PublicEndpoint = {
  name: string;
  context_length?: number;
  max_output?: number;
  streams?: boolean;
  abilities?: string[];
  quantization?: string;
  verified?: boolean;
  pricing?: PublicModelPricing;
};

function Price({ model, item }: { model: PublicModel; item: "input" | "output" }) {
  return <PricingPrice pricing={model.pricing} item={item} />;
}

// hasPrice says whether a model's answer carries any price at all. A site that keeps its prices to signed-in members
// leaves every model's and endpoint's pricing out, so a page can tell that state from a missing field on an older node,
// which always sent the model's price.
const hasPrice = (model: PublicModel) =>
  model.pricing !== undefined || (model.endpoints ?? []).some((endpoint) => endpoint.pricing !== undefined);

export const modelAbilities = [
  "tools",
  "images",
  "documents",
  "reasoning",
  "prompt_cache",
  "structured_output",
  "web_search",
  "code_execution",
  "file_search",
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
// filters and the request, and both the website and the console use this component so the two read the same. A list
// with no prices anywhere is the site keeping them to signed-in members, so the table drops its price columns and says
// so once, instead of marking every row's price unavailable.
export function ModelCatalog({ models, note }: { models: PublicModel[]; note?: ReactNode }) {
  const { t } = useTranslation("common");
  const [open, setOpen] = useState<string | null>(null);
  const showPrices = models.some(hasPrice);
  const columns = showPrices ? 8 : 6;
  return (
    <div className="space-y-4">
      {models.length > 0 && !showPrices ? (
        <p className="max-w-prose text-ui text-t2">{t("publicModels.pricesNotPublic")}</p>
      ) : null}
      <Table label={t("publicModels.title")}>
        <thead>
          <tr>
            <Th>{t("publicModels.search")}</Th>
            <Th className="hidden md:table-cell">{t("publicModels.author")}</Th>
            <Th className="hidden text-right lg:table-cell">{t("publicModels.context")}</Th>
            <Th className="hidden text-right xl:table-cell">{t("publicModels.maxOutput")}</Th>
            <Th className="hidden 2xl:table-cell">{t("publicModels.ability")}</Th>
            <PriceHeads show={showPrices} />
            <Th />
          </tr>
        </thead>
        <tbody>
          {models.map((model) => (
            <ModelRow
              key={model.id}
              model={model}
              open={open === model.id}
              showPrices={showPrices}
              columns={columns}
              onToggle={() => setOpen(open === model.id ? null : model.id)}
            />
          ))}
        </tbody>
      </Table>
      {note && showPrices ? <p className="max-w-prose text-ui text-t3">{note}</p> : null}
    </div>
  );
}

// ModelRow is a model and, when it is open, its details in the row right under it, where the person who opened them is
// looking; a list of a hundred models would otherwise show them far below the button.
function ModelRow({
  model,
  open,
  showPrices,
  columns,
  onToggle,
}: {
  model: PublicModel;
  open: boolean;
  showPrices: boolean;
  columns: number;
  onToggle: () => void;
}) {
  const details = useId();
  return (
    <>
      <ModelSummary model={model} open={open} showPrices={showPrices} details={details} onToggle={onToggle} />
      {open ? (
        <tr>
          <td colSpan={columns} className="border-b border-b1 bg-s2">
            <ModelDetails id={details} model={model} showPrices={showPrices} />
          </td>
        </tr>
      ) : null}
    </>
  );
}

function ModelSummary({
  model,
  open,
  showPrices,
  details,
  onToggle,
}: {
  model: PublicModel;
  open: boolean;
  showPrices: boolean;
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
        {showPrices ? (
          <div className="mt-1 text-label tabular-nums sm:hidden">
            <span className="text-t3">{t("publicModels.item.input")} </span>
            <Price model={model} item="input" />
          </div>
        ) : null}
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
      {showPrices ? (
        <>
          <Td className="hidden text-right tabular-nums sm:table-cell">
            <Price model={model} item="input" />
          </Td>
          <Td className="hidden text-right tabular-nums sm:table-cell">
            <Price model={model} item="output" />
          </Td>
        </>
      ) : null}
      <Td className="text-right">
        <Button size="sm" variant="ghost" aria-expanded={open} aria-controls={details} onClick={onToggle}>
          {t(open ? "publicModels.hide" : "publicModels.details")}
          <Icon name={open ? "down" : "right"} />
        </Button>
      </Td>
    </tr>
  );
}

function ModelDetails({ id, model, showPrices }: { id: string; model: PublicModel; showPrices: boolean }) {
  const { t } = useTranslation("common");
  const pricing = model.pricing;
  const rates = pricing?.rates ?? [];
  return (
    // w-0 min-w-full: the details take the table's width without widening it on a narrow screen.
    <section id={id} aria-label={model.name} className="w-0 min-w-full pb-1">
      <ModelFacts model={model} />
      <EndpointSection model={model} showPrices={showPrices} />
      {showPrices ? (
        <div className="mt-4 border-t border-b1">
          <div className="px-4 pt-4">
            <h3 className="text-ui text-t1">{t("publicModels.modelPrice")}</h3>
            <p className="mt-1 text-label text-t3">{t("publicModels.modelPriceNote")}</p>
          </div>
          {!pricing ? (
            <p className="px-4 py-3 text-t3">{t("publicModels.priceUnavailable")}</p>
          ) : rates.length === 0 ? (
            <p className="px-4 py-3 text-t3">{t("publicModels.noRates")}</p>
          ) : (
            <RateTable
              title={`${model.name} ${t("publicModels.modelPrice")}`}
              currency={pricing.currency}
              basis={pricing.basis}
              rates={rates}
            />
          )}
          {pricing?.search_engines?.length ? (
            <SearchEnginePrices
              title={`${model.name} ${t("publicModels.searchEngines")}`}
              currency={pricing.currency}
              engines={pricing.search_engines}
            />
          ) : null}
        </div>
      ) : null}
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
