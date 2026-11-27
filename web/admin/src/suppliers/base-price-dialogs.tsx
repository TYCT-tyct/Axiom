import { type AdminSchemas, ApiError, adminApi, unwrapChange } from "@axiom/api";
import { DataState, SegmentedControl, Stack, Text } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "../change";
import { RatesFields, useInvalid } from "../models/price-fields";
import { Rates } from "../models/prices";
import { modelQuery } from "../queries";
import { type BaseModel, type BaseTarget, confirmation, officialIn, readBase } from "./base-price";

type Supplier = AdminSchemas["Supplier"];
type Schedule = AdminSchemas["PriceSchedule"];

const base = "/api/admin/v1/suppliers/{supplier_id}";
const params = (supplier: Supplier, key: string) => ({
  path: { supplier_id: supplier.id },
  header: { "Idempotency-Key": key },
});

// Confirms the base price of a model the supplier publishes no price for: the catalog's official price by default, or
// prices typed item by item. The cost it makes is the server's to work out (the base in the supplier's unit, times the
// group's ratio, each rounded up, within the unit price limit), so the dialog says how and the endpoint's price lines
// show the result after the next sync round. The server keeps the base in the supplier's unit, so it needs the unit
// value; without one the dialog says so and links to confirming it.
export function ConfirmBaseDialog({
  supplier,
  target,
  onClose,
}: {
  supplier: Supplier;
  target: BaseTarget;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const invalid = useInvalid();
  const model = useQuery(modelQuery(target.model_id));
  const currency = supplier.settlement_currency;
  const noUnit = supplier.unit_value === undefined;
  return (
    <ChangeDialog
      operation={`supplier-base-price:${supplier.id}:${target.group_id}:${target.upstream_name}`}
      title={t("upstream.basePrice.title", { name: target.upstream_name })}
      intro={
        <>
          <p>{t("upstream.basePrice.body", { supplier: supplier.name, model: target.model_slug ?? "—" })}</p>
          <p>{t("upstream.basePrice.formula")}</p>
        </>
      }
      warn={noUnit ? <UnitMissing supplier={supplier} /> : undefined}
      action={t("upstream.basePrice.open")}
      ready={!noUnit && model.isSuccess}
      fields={() => (
        <DataState query={model} compact>
          {(found) => <BaseFields official={officialIn(found, currency)} currency={currency} />}
        </DataState>
      )}
      read={(values) => readBase(values, currency, invalid)}
      send={(choice, { reason, key }) =>
        unwrapChange(
          adminApi.POST(`${base}/base-prices`, {
            params: params(supplier, key),
            body: confirmation(target, choice, reason),
          }),
        )
      }
      explain={(error) =>
        error instanceof ApiError && (error.code === "conflict" || error.code === "unprocessable")
          ? t(`upstream.basePrice.refused.${error.code}`)
          : undefined
      }
      done={() => <p>{t("upstream.basePrice.done")}</p>}
      onClose={onClose}
    />
  );
}

function UnitMissing({ supplier }: { supplier: Supplier }) {
  const { t } = useTranslation();
  return (
    <>
      {t("upstream.basePrice.unitMissingBody")}{" "}
      <Link to="/suppliers/$supplierId" params={{ supplierId: supplier.id }} search={{ tab: "settings" }}>
        {t("upstream.basePrice.unitAction")}
      </Link>
    </>
  );
}

type Source = "official" | "entered";

// The choice of where the base comes from: the official price as the catalog has it, or prices typed item by item,
// prefilled from its standard prices. The typed fields stay mounted while the official price is chosen, only hidden,
// so going back to them finds what was typed; the source field says which of the two is sent.
export function BaseFields({ official, currency }: { official?: Schedule; currency: string }) {
  const { t } = useTranslation();
  const [source, setSource] = useState<Source>(official ? "official" : "entered");
  return (
    <Stack gap={16}>
      <input type="hidden" name="source" value={source} />
      {official ? (
        <SegmentedControl
          label={t("upstream.basePrice.source")}
          value={source}
          options={(["official", "entered"] as const).map((value) => ({
            value,
            label: t(`upstream.basePrice.${value}`),
          }))}
          onChange={setSource}
        />
      ) : (
        <Text tone="warn">{t("upstream.basePrice.noOfficial", { currency })}</Text>
      )}
      {official && source === "official" ? (
        <Stack gap={8}>
          <Text size="sm" tone="muted">
            {t("upstream.basePrice.officialPrice")}
          </Text>
          <Rates schedule={official} />
        </Stack>
      ) : null}
      <div hidden={source === "official"}>
        <Stack gap={8}>
          <Text size="sm" tone="muted">
            {t("upstream.basePrice.enteredHint", { currency })}
          </Text>
          <RatesFields schedule={{ v: 1, currency, rates: official?.rates ?? {} }} />
        </Stack>
      </div>
    </Stack>
  );
}

// Takes a confirmed base away: at the next sync round the model's cost is unknown again and its endpoint leaves routing.
export function ClearBaseDialog({
  supplier,
  target,
  onClose,
}: {
  supplier: Supplier;
  target: BaseModel;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`supplier-base-price-clear:${supplier.id}:${target.group_id}:${target.upstream_name}`}
      title={t("upstream.basePrice.clearTitle", { name: target.upstream_name })}
      intro={<p>{t("upstream.basePrice.clearBody")}</p>}
      action={t("upstream.basePrice.clear")}
      danger
      read={() => null}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST(`${base}/base-price-removals`, {
            params: params(supplier, key),
            body: { items: [{ group_id: target.group_id, upstream_name: target.upstream_name }], reason },
          }),
        )
      }
      done={(removed) => (
        <p>{t(removed.data.length > 0 ? "upstream.basePrice.cleared" : "upstream.basePrice.clearedNone")}</p>
      )}
      onClose={onClose}
    />
  );
}
