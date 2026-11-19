import { type ReactNode, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { classes } from "../classes";
import { useFormat } from "../format";
import { Button } from "./action-button";
import { SelectField, TextField } from "./form";
import { Icon } from "./icon";
import { Cluster, Stack } from "./layout";
import { EndpointSection } from "./model-endpoints";
import {
  label,
  ModelSection,
  PriceHeads,
  PricingPrice,
  type PublicModelPricing,
  RateTable,
  SearchEnginePrices,
} from "./model-pricing";
import s from "./models.module.css";
import { Table, Td, Th } from "./table";
import { Tag as Badge } from "./tag";
import { Text } from "./text";

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
      className={s.filters}
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
      <Cluster>
        <Button variant="primary" size="md" type="submit">
          {t("publicModels.apply")}
        </Button>
        {reset}
      </Cluster>
    </form>
  );
}

// ModelCatalog is the list and the details of the selected model. It is presentational: the page owns the URL
// filters and the request, and both the website and the console use this component so the two read the same. A list
// with no prices anywhere is the site keeping them to signed-in members, so the table drops its price columns and says
// so once, instead of marking every row's price unavailable. count, when the page has it, is the line above the table
// that says how many models the filters left.
export function ModelCatalog({ models, note, count }: { models: PublicModel[]; note?: ReactNode; count?: string }) {
  const { t } = useTranslation("common");
  const [open, setOpen] = useState<string | null>(null);
  const showPrices = models.some(hasPrice);
  const columns = showPrices ? 8 : 6;
  return (
    <>
      {count ? (
        <div className={s.count}>
          <p className={s.countText}>{count}</p>
        </div>
      ) : null}
      <Stack>
        {models.length > 0 && !showPrices ? (
          <p className={classes(s.note, s.muted)}>{t("publicModels.pricesNotPublic")}</p>
        ) : null}
        <Table label={t("publicModels.title")}>
          <thead>
            <tr>
              <Th>{t("publicModels.search")}</Th>
              <Th from="md">{t("publicModels.author")}</Th>
              <Th align="end" from="lg">
                {t("publicModels.context")}
              </Th>
              <Th align="end" from="xl">
                {t("publicModels.maxOutput")}
              </Th>
              <Th from="2xl">{t("publicModels.ability")}</Th>
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
        {note && showPrices ? <p className={s.note}>{note}</p> : null}
      </Stack>
    </>
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
          <td colSpan={columns} className={s.detailsCell}>
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
    <tr className={s.row}>
      <Td>
        <div className={s.name}>
          <p className={s.truncate} title={model.name}>
            {model.name}
          </p>
          <p className={s.id} title={model.id}>
            {model.id}
          </p>
          {/* Below sm the price columns are hidden, so the input price reads under the name and Details stays in view. */}
          {showPrices ? (
            <div className={s.inlinePrice}>
              <Text as="span" size="sm" tone="faint">
                {t("publicModels.item.input")}{" "}
              </Text>
              <Price model={model} item="input" />
            </div>
          ) : null}
        </div>
      </Td>
      <Td from="md">
        <div className={s.author}>
          <p className={s.truncate} title={model.author}>
            {model.author}
          </p>
        </div>
      </Td>
      <Td align="end" from="lg">
        {model.context_length ? format.number(model.context_length) : "—"}
      </Td>
      <Td align="end" from="xl">
        {model.max_output ? format.number(model.max_output) : "—"}
      </Td>
      <Td from="2xl">
        <span className={s.abilities}>
          {(model.abilities ?? []).map((ability) => (
            <Badge key={ability}>{label(t, "abilityNames", ability)}</Badge>
          ))}
        </span>
      </Td>
      {showPrices ? (
        <>
          <Td align="end" from="sm">
            <Price model={model} item="input" />
          </Td>
          <Td align="end" from="sm">
            <Price model={model} item="output" />
          </Td>
        </>
      ) : null}
      <Td align="end">
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
    // width 0 and min-width 100%: the details take the table's width without widening it on a narrow screen.
    <section id={id} aria-label={model.name} className={s.details}>
      <ModelFacts model={model} />
      <EndpointSection model={model} showPrices={showPrices} />
      {showPrices ? (
        <ModelSection title={t("publicModels.modelPrice")} note={t("publicModels.modelPriceNote")}>
          {!pricing ? (
            <div className={s.pad}>
              <Text tone="faint">{t("publicModels.priceUnavailable")}</Text>
            </div>
          ) : rates.length === 0 ? (
            <div className={s.pad}>
              <Text tone="faint">{t("publicModels.noRates")}</Text>
            </div>
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
        </ModelSection>
      ) : null}
    </section>
  );
}

function ModelFacts({ model }: { model: PublicModel }) {
  const { t } = useTranslation("common");
  const format = useFormat();
  const abilities = model.abilities ?? [];
  return (
    <dl className={s.facts}>
      <dt className={s.term}>{t("publicModels.author")}</dt>
      <dd className={s.def}>{model.author}</dd>
      <dt className={s.term}>{t("publicModels.context")}</dt>
      <dd className={classes(s.def, s.num)}>{model.context_length ? format.number(model.context_length) : "—"}</dd>
      <dt className={s.term}>{t("publicModels.maxOutput")}</dt>
      <dd className={classes(s.def, s.num)}>{model.max_output ? format.number(model.max_output) : "—"}</dd>
      <dt className={s.term}>{t("publicModels.streams")}</dt>
      <dd className={s.def}>
        {model.streams === undefined ? "—" : t(model.streams ? "publicModels.yes" : "publicModels.no")}
      </dd>
      <dt className={s.term}>{t("publicModels.ability")}</dt>
      <dd className={classes(s.def, s.tags)}>
        {abilities.length === 0
          ? "—"
          : abilities.map((ability) => <Badge key={ability}>{label(t, "abilityNames", ability)}</Badge>)}
      </dd>
    </dl>
  );
}
