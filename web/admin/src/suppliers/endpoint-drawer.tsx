import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import {
  ActionButton,
  Cluster,
  DrawerFacts,
  DrawerSection,
  LinkButton,
  SidePanel,
  Stack,
  Tag,
  Text,
  useFormat,
} from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { showRate } from "../models/price-fields";
import { Code } from "../parts";
import { EndpointEnabledDialog, QuantizationDialog } from "./dialogs";
import { endpointState, testFormats } from "./endpoint-test";
import { EndpointTestDialog } from "./endpoint-test-dialog";
import { BuiltState, Rate, useTraffic } from "./parts";
import { endpointCostQuery } from "./queries";

type Supplier = AdminSchemas["Supplier"];
type Endpoint = AdminSchemas["SupplierEndpoint"];
type Cost = AdminSchemas["EndpointCost"];
type CostLine = AdminSchemas["CostLine"];

// An endpoint's drawer (adP_ep in admin_pages_a.js): its formats, precision and success, and its cost as price lines
// (adPriceRows): what each metering item costs, under what condition, where the plan came from and what it is the
// product of; for an upstream that charges by the request while the model sells by the token, where it breaks even.
export function EndpointDrawer({
  supplier,
  endpoint,
  line,
  onClose,
}: {
  supplier: Supplier;
  endpoint: Endpoint;
  line: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const traffic = useTraffic();
  const manage = can(useSession(), "suppliers.manage") && supplier.status !== "removed";
  const [open, setOpen] = useState<"enable" | "test" | "quantization" | null>(null);
  const testable = endpointState(endpoint) === "ready" && testFormats(endpoint.formats).length > 0;
  return (
    <SidePanel
      drawer
      title={endpoint.upstream_name}
      subtitle={`${line}，${supplier.name}`}
      onClose={onClose}
      footer={
        manage ? (
          <Cluster justify="end">
            <ActionButton variant="gho" onClick={() => setOpen("enable")}>
              {t(endpoint.enabled ? "upstream.endpoint.disable" : "upstream.endpoint.enable")}
            </ActionButton>
            {testable ? (
              <ActionButton variant="pri" onClick={() => setOpen("test")}>
                {t("upstream.endpoint.test")}
              </ActionButton>
            ) : null}
          </Cluster>
        ) : undefined
      }
    >
      <DrawerFacts
        rows={[
          [
            t("upstream.endpoint.format"),
            endpoint.formats.map((format) => t(`suppliers.test.format_${format}`)).join("、"),
          ],
          [
            t("upstream.endpoint.quantization"),
            <Cluster key="q" gap={8}>
              {endpoint.quantization ?? t("suppliers.quantization.none")}
              {manage ? (
                <LinkButton onClick={() => setOpen("quantization")}>{t("upstream.endpoint.change")}</LinkButton>
              ) : null}
            </Cluster>,
          ],
          [t("upstream.endpoint.rate"), <Rate key="rate" rate={traffic?.ofGroup(endpoint.group_id).rate} />],
        ]}
      />
      <DrawerSection title={t("upstream.endpoint.cost")}>
        <CostSection supplier={supplier} endpoint={endpoint} />
      </DrawerSection>
      {open === "enable" ? (
        <EndpointEnabledDialog supplier={supplier} endpoint={endpoint} group={line} onClose={() => setOpen(null)} />
      ) : null}
      {open === "test" ? <EndpointTestDialog endpoint={endpoint} group={line} onClose={() => setOpen(null)} /> : null}
      {open === "quantization" ? (
        <QuantizationDialog supplier={supplier} endpoint={endpoint} onClose={() => setOpen(null)} />
      ) : null}
    </SidePanel>
  );
}

function CostSection({ supplier, endpoint }: { supplier: Supplier; endpoint: Endpoint }) {
  const cost = useQuery(endpointCostQuery(supplier.id, endpoint.id));
  return (
    <BuiltState query={cost} compact>
      {(plan) => <PriceLines plan={plan} />}
    </BuiltState>
  );
}

function PriceLines({ plan }: { plan: Cost }) {
  const { t } = useTranslation();
  const words = t as unknown as T;
  if (plan.cost_state !== "known" || plan.lines.length === 0) {
    return (
      <Text tone="warn">
        {t(plan.cost_state === "conflict" ? "upstream.endpoint.conflict" : "upstream.endpoint.noCost")}
      </Text>
    );
  }
  return (
    <>
      <DrawerFacts
        rows={plan.lines.map((line) => [
          `${words(`upstream.item.${line.item}`)}，${condition(words, line)}`,
          <Stack key="price" gap={4}>
            <PriceText line={line} currency={plan.settlement_currency} />
            <Text as="span" size="sm" tone="muted">
              {factors(words, line)}
            </Text>
          </Stack>,
        ])}
      />
      {plan.source ? <Tag>{t(`upstream.endpoint.sources.${plan.source}`)}</Tag> : null}
      {plan.break_even ? <BreakEven value={plan.break_even} /> : null}
    </>
  );
}

// The drawer's words by a key made up at run time (an item, a unit, a factor): the catalog has every one of them, and a
// key built from a value is more than the typed t can check.
type T = (key: string, options?: Record<string, unknown>) => string;

// The condition a price holds under, after its item: its unit, and the context tier or service tier it is for.
function condition(t: T, line: CostLine) {
  const parts: string[] = [t(`upstream.endpoint.per.${line.unit}`)];
  if (line.above !== undefined) parts.push(t("upstream.endpoint.above", { count: line.above }));
  if (line.service_tier && line.service_tier !== "standard") {
    parts.push(t("upstream.endpoint.tier", { tier: t(`models.tier.${line.service_tier}`) }));
  }
  return parts.join("，");
}

// What the price is the product of, written out: 模型倍率 1.5 × 分组倍率 0.7 × 每单位 1.00.
function factors(t: T, line: CostLine) {
  return line.factors
    .map((factor) => t(`upstream.endpoint.factor.${factor.kind}`, { value: factor.value }))
    .join(" × ");
}

function PriceText({ line, currency }: { line: CostLine; currency: string }) {
  const { decimal } = useFormat();
  // A token's price is read per million tokens; the item's own unit otherwise (showRate knows which).
  const shown = (price: string) => decimal(line.unit === "token" ? showRate("input", price) : price);
  if (line.price === undefined) {
    return <Code>{line.native_price === undefined ? "—" : shown(line.native_price)}</Code>;
  }
  return (
    <Text as="span" numeric medium>
      {`${shown(line.price)} ${currency}`}
    </Text>
  );
}

function BreakEven({ value }: { value: NonNullable<Cost["break_even"]> }) {
  const { t } = useTranslation();
  const format = useFormat();
  const tokens = format.number(value.tokens);
  return (
    <Text
      size="sm"
      tone={value.mean_tokens_24h !== undefined && value.mean_tokens_24h < value.tokens ? "warn" : "muted"}
    >
      {value.mean_tokens_24h === undefined
        ? t("upstream.endpoint.breakEvenNoMean", { tokens })
        : t("upstream.endpoint.breakEven", { tokens, mean: format.number(value.mean_tokens_24h) })}
    </Text>
  );
}
