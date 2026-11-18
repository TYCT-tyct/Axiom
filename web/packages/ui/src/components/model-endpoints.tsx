import { useTranslation } from "react-i18next";
import { useFormat } from "../format";
import { label, PriceHeads, PricingPrice, RateTable, SearchEnginePrices } from "./model-pricing";
import type { PublicEndpoint, PublicModel } from "./models";
import { Badge } from "./panel";
import { Table, Td, Th } from "./table";

// EndpointSection lists every group that serves the model under the codename routing accepts. Each row carries the
// attributes that have no column at this width, its own base prices and a full schedule, so a reader can see that the
// headline is the cheapest endpoint, not the only one. showPrices is false when the site keeps its prices to signed-in
// members: the endpoints and their attributes stay, the price columns and schedules go.
export function EndpointSection({ model, showPrices }: { model: PublicModel; showPrices: boolean }) {
  const { t } = useTranslation("common");
  if (model.endpoints === undefined) {
    return (
      <p className="mt-4 border-t border-b1 px-4 pt-4 text-ui text-t3">{t("publicModels.endpointsUnavailable")}</p>
    );
  }
  if (model.endpoints.length === 0) {
    return <p className="mt-4 border-t border-b1 px-4 pt-4 text-ui text-t3">{t("publicModels.noEndpoints")}</p>;
  }
  return (
    <div className="mt-4 border-t border-b1">
      <div className="px-4 pt-4">
        <h3 className="text-ui text-t1">{t("publicModels.endpoints")}</h3>
        <p className="mt-1 text-label text-t3">{t("publicModels.endpointsNote")}</p>
      </div>
      <Table label={t("publicModels.endpoints")}>
        <thead>
          <tr>
            <Th>{t("publicModels.endpoint")}</Th>
            <Th className="hidden text-right md:table-cell">{t("publicModels.context")}</Th>
            <Th className="hidden text-right lg:table-cell">{t("publicModels.maxOutput")}</Th>
            <Th className="hidden 2xl:table-cell">{t("publicModels.ability")}</Th>
            <PriceHeads show={showPrices} />
          </tr>
        </thead>
        <tbody>
          {model.endpoints.map((endpoint) => (
            <EndpointRow key={endpoint.name} model={model} endpoint={endpoint} showPrices={showPrices} />
          ))}
        </tbody>
      </Table>
    </div>
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
        <Td className="hidden text-right whitespace-nowrap tabular-nums md:table-cell">
          {endpoint.context_length ? format.number(endpoint.context_length) : "—"}
        </Td>
        <Td className="hidden text-right whitespace-nowrap tabular-nums lg:table-cell">
          {endpoint.max_output ? format.number(endpoint.max_output) : "—"}
        </Td>
        <Td className="hidden 2xl:table-cell">
          <EndpointAbilities endpoint={endpoint} />
        </Td>
        {showPrices ? (
          <>
            <Td className="hidden text-right tabular-nums sm:table-cell">
              <PricingPrice pricing={endpoint.pricing} item="input" />
            </Td>
            <Td className="hidden text-right tabular-nums sm:table-cell">
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
  const { t } = useTranslation("common");
  const format = useFormat();
  return (
    <Td className="max-w-56">
      <p className="truncate font-mono text-t1" title={endpoint.name}>
        {endpoint.name}
      </p>
      <span className="mt-1 flex flex-wrap gap-1">
        <Badge tone={endpoint.verified ? "ok" : "neutral"} icon={endpoint.verified ? "check" : undefined}>
          {t(endpoint.verified ? "publicModels.verified" : "publicModels.unverified")}
        </Badge>
        {endpoint.quantization ? (
          <Badge>
            <span>{t("publicModels.quantization")}</span>
            <span className="font-mono">{endpoint.quantization}</span>
          </Badge>
        ) : null}
      </span>
      {/* Each fallback disappears at the width where its column appears: context at md, max output at lg, abilities at
          2xl. Streaming has no column, so it stays here at every width. */}
      <p className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-label text-t3 tabular-nums">
        <span className="md:hidden">
          {t("publicModels.context")} {endpoint.context_length ? format.number(endpoint.context_length) : "—"}
        </span>
        <span className="lg:hidden">
          {t("publicModels.maxOutput")} {endpoint.max_output ? format.number(endpoint.max_output) : "—"}
        </span>
        <span>
          {t("publicModels.streams")}{" "}
          {endpoint.streams === undefined ? "—" : t(endpoint.streams ? "publicModels.yes" : "publicModels.no")}
        </span>
      </p>
      <div className="mt-1 flex flex-wrap items-center gap-1 text-label text-t3 2xl:hidden">
        <span>{t("publicModels.ability")}</span>
        <EndpointAbilities endpoint={endpoint} fallback />
      </div>
      {showPrices ? (
        <div className="mt-1 grid grid-cols-2 gap-x-3 text-label tabular-nums sm:hidden">
          <span>
            <span className="text-t3">{t("publicModels.item.input")} </span>
            <PricingPrice pricing={endpoint.pricing} item="input" />
          </span>
          <span>
            <span className="text-t3">{t("publicModels.item.output")} </span>
            <PricingPrice pricing={endpoint.pricing} item="output" />
          </span>
        </div>
      ) : null}
    </Td>
  );
}

function EndpointAbilities({ endpoint, fallback = false }: { endpoint: PublicEndpoint; fallback?: boolean }) {
  const { t } = useTranslation("common");
  const abilities = endpoint.abilities ?? [];
  if (abilities.length === 0) {
    return "—";
  }
  return (
    <span className={fallback ? "flex flex-wrap gap-1" : "flex max-w-64 flex-wrap gap-1"}>
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
      <Td colSpan={6} className="p-0">
        <details className="px-3 pb-3 sm:px-4">
          <summary className="cursor-pointer py-2 text-label text-t3">
            {t("publicModels.endpointPriceDetails")} <span className="font-mono">{endpoint.name}</span>
          </summary>
          {!pricing || (rates.length === 0 && engines.length === 0) ? (
            <p className="py-2 text-label text-t3">{t("publicModels.priceUnavailable")}</p>
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
                <div className="mt-2">
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
