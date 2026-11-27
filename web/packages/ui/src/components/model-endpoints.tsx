import { useTranslation } from "react-i18next";
import { classes } from "../classes";
import { useFormat } from "../format";
import s from "./model-endpoints.module.css";
import { label, ModelSection, PriceHeads, PricingPrice, RateTable, SearchEnginePrices } from "./model-pricing";
import type { PublicEndpoint, PublicModel } from "./models";
import { Table, Td, Th } from "./table";
import { Tag as Badge } from "./tag";
import { Text } from "./text";

// EndpointSection lists every group that serves the model under the codename routing accepts. Each row carries the
// attributes that have no column at this width, its own base prices and a full schedule, so a reader can see that the
// headline is the cheapest endpoint, not the only one. showPrices is false when the site keeps its prices to signed-in
// members: the endpoints and their attributes stay, the price columns and schedules go.
export function EndpointSection({ model, showPrices }: { model: PublicModel; showPrices: boolean }) {
  const { t } = useTranslation("common");
  if (model.endpoints === undefined) {
    return <ModelSection note={t("publicModels.endpointsUnavailable")} />;
  }
  if (model.endpoints.length === 0) {
    return <ModelSection note={t("publicModels.noEndpoints")} />;
  }
  return (
    <ModelSection title={t("publicModels.endpoints")} note={t("publicModels.endpointsNote")}>
      <Table label={t("publicModels.endpoints")}>
        <thead>
          <tr>
            <Th>{t("publicModels.endpoint")}</Th>
            <Th align="end" from="md">
              {t("publicModels.context")}
            </Th>
            <Th align="end" from="lg">
              {t("publicModels.maxOutput")}
            </Th>
            <Th from="2xl">{t("publicModels.ability")}</Th>
            <PriceHeads show={showPrices} />
          </tr>
        </thead>
        <tbody>
          {model.endpoints.map((endpoint) => (
            <EndpointRow key={endpoint.name} model={model} endpoint={endpoint} showPrices={showPrices} />
          ))}
        </tbody>
      </Table>
    </ModelSection>
  );
}

function EndpointRow({
  model,
  endpoint,
  showPrices,
}: {
  model: PublicModel;
  endpoint: PublicEndpoint;
  showPrices: boolean;
}) {
  const format = useFormat();
  return (
    <>
      <tr>
        <EndpointIdentity endpoint={endpoint} showPrices={showPrices} />
        <Td align="end" from="md">
          {endpoint.context_length ? format.number(endpoint.context_length) : "—"}
        </Td>
        <Td align="end" from="lg">
          {endpoint.max_output ? format.number(endpoint.max_output) : "—"}
        </Td>
        <Td from="2xl">
          <EndpointAbilities endpoint={endpoint} />
        </Td>
        {showPrices ? (
          <>
            <Td align="end" from="sm">
              <PricingPrice pricing={endpoint.pricing} item="input" />
            </Td>
            <Td align="end" from="sm">
              <PricingPrice pricing={endpoint.pricing} item="output" />
            </Td>
          </>
        ) : null}
      </tr>
      {showPrices ? <EndpointPriceDetails model={model} endpoint={endpoint} /> : null}
    </>
  );
}

function EndpointIdentity({ endpoint, showPrices }: { endpoint: PublicEndpoint; showPrices: boolean }) {
  const { t, i18n } = useTranslation("common");
  const format = useFormat();
  return (
    <Td>
      <div className={s.name}>
        <p className={s.codename} title={endpoint.name}>
          {endpoint.name}
        </p>
        <span className={s.tags}>
          <Badge tone={endpoint.verified ? "ok" : "neutral"} icon={endpoint.verified ? "check" : undefined}>
            {t(endpoint.verified ? "publicModels.verified" : "publicModels.unverified")}
          </Badge>
          {endpoint.quantization ? (
            <Badge>
              <span>{t("publicModels.quantization")}</span>
              <span className={s.mono}>{endpoint.quantization}</span>
            </Badge>
          ) : null}
          {endpoint.source_class ? <Badge>{className(endpoint.source_class, i18n.language)}</Badge> : null}
          {endpoint.quality_score !== undefined ? (
            <Badge>{t("publicModels.score", { score: format.decimal(String(endpoint.quality_score)) })}</Badge>
          ) : null}
        </span>
        {/* Each fallback disappears at the width where its column appears: context at md, max output at lg, abilities at
          2xl. Streaming has no column, so it stays here at every width. */}
        <p className={s.attributes}>
          <span className={s.contextFallback}>
            {t("publicModels.context")} {endpoint.context_length ? format.number(endpoint.context_length) : "—"}
          </span>
          <span className={s.outputFallback}>
            {t("publicModels.maxOutput")} {endpoint.max_output ? format.number(endpoint.max_output) : "—"}
          </span>
          <span>
            {t("publicModels.streams")}{" "}
            {endpoint.streams === undefined ? "—" : t(endpoint.streams ? "publicModels.yes" : "publicModels.no")}
          </span>
        </p>
        <Measured endpoint={endpoint} />
        <div className={s.fallback}>
          <span>{t("publicModels.ability")}</span>
          <EndpointAbilities endpoint={endpoint} fallback />
        </div>
        {showPrices ? (
          <div className={s.prices}>
            <span>
              <Text as="span" size="sm" tone="faint">
                {t("publicModels.item.input")}{" "}
              </Text>
              <PricingPrice pricing={endpoint.pricing} item="input" />
            </span>
            <span>
              <Text as="span" size="sm" tone="faint">
                {t("publicModels.item.output")}{" "}
              </Text>
              <PricingPrice pricing={endpoint.pricing} item="output" />
            </span>
          </div>
        ) : null}
      </div>
    </Td>
  );
}

// A source class is named in the page's language, falling back to its English name.
const className = (c: { name: string; name_zh: string }, language: string) =>
  (language.startsWith("zh") && c.name_zh) || c.name;

// Measured is what real calls showed of the endpoint: uptime over three windows, and the median first token and speed
// over thirty minutes. A value not known yet is a dash, never a zero.
function Measured({ endpoint }: { endpoint: PublicEndpoint }) {
  const { t } = useTranslation("common");
  const format = useFormat();
  const percent = (value?: number) => (value === undefined ? "—" : `${format.decimal(String(value))}%`);
  const median = (stats: PublicEndpoint["latency_last_30m"], unit: "milliseconds" | "tokensPerSecond") =>
    stats ? t(`publicModels.${unit}`, { value: format.decimal(String(stats.p50)) }) : "—";
  return (
    <p className={s.attributes}>
      <span>
        {`${t("publicModels.uptime")} ${[endpoint.uptime_last_5m, endpoint.uptime_last_30m, endpoint.uptime_last_1d].map(percent).join(" / ")}`}
      </span>
      <span>{`${t("publicModels.latency")} ${median(endpoint.latency_last_30m, "milliseconds")}`}</span>
      <span>{`${t("publicModels.throughput")} ${median(endpoint.throughput_last_30m, "tokensPerSecond")}`}</span>
    </p>
  );
}

function EndpointAbilities({ endpoint, fallback = false }: { endpoint: PublicEndpoint; fallback?: boolean }) {
  const { t } = useTranslation("common");
  const abilities = endpoint.abilities ?? [];
  if (abilities.length === 0) {
    return "—";
  }
  return (
    <span className={classes(s.abilities, fallback && s.free)}>
      {abilities.map((ability) => (
        <Badge key={ability}>{label(t, "abilityNames", ability)}</Badge>
      ))}
    </span>
  );
}

function EndpointPriceDetails({ model, endpoint }: { model: PublicModel; endpoint: PublicEndpoint }) {
  const { t } = useTranslation("common");
  const pricing = endpoint.pricing;
  const rates = pricing?.rates ?? [];
  const engines = pricing?.search_engines ?? [];
  return (
    <tr>
      <Td colSpan={6}>
        <details>
          <summary className={s.summary}>
            {t("publicModels.endpointPriceDetails")} <span className={s.mono}>{endpoint.name}</span>
          </summary>
          {!pricing || (rates.length === 0 && engines.length === 0) ? (
            <Text size="sm" tone="faint">
              {t("publicModels.priceUnavailable")}
            </Text>
          ) : (
            <>
              {rates.length > 0 ? (
                <RateTable
                  title={`${model.name} ${endpoint.name} ${t("publicModels.endpointPrice")}`}
                  currency={pricing.currency}
                  basis={pricing.basis}
                  rates={rates}
                />
              ) : null}
              {engines.length > 0 ? (
                <div className={s.engines}>
                  <SearchEnginePrices
                    title={`${model.name} ${endpoint.name} ${t("publicModels.searchEngines")}`}
                    currency={pricing.currency}
                    engines={engines}
                  />
                </div>
              ) : null}
            </>
          )}
        </details>
      </Td>
    </tr>
  );
}
