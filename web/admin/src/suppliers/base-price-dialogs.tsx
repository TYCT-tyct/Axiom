import { type AdminSchemas, ApiError, adminApi, unwrapChange } from "@axiom/api";
import { DataState, SegmentedControl, Stack, Text } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, isProblem } from "../change";
import { RatesFields, readSchedule, useInvalid } from "../models/price-fields";
import { Rates } from "../models/prices";
import { modelQuery, supplierQuery } from "../queries";
import { type BaseModel, confirmation, officialIn, readBase, scaled } from "./base-price";

type Supplier = AdminSchemas["Supplier"];
type Schedule = AdminSchemas["PriceSchedule"];

// The model a base is confirmed for: its group and upstream name, and the catalog model the name maps to now, whose
// official price is the default.
export type BaseTarget = BaseModel & { model_id: string };

export const baseOf = (endpoint: AdminSchemas["SupplierEndpoint"]): BaseTarget => ({
  group_id: endpoint.group_id,
  upstream_name: endpoint.upstream_name,
  model_id: endpoint.model_id,
});

const base = "/api/admin/v1/suppliers/{supplier_id}";
const params = (supplier: Supplier, key: string) => ({
  path: { supplier_id: supplier.id },
  header: { "Idempotency-Key": key },
});

// Confirms the base price of a model the supplier publishes no price for: the catalog's official price by default, or
// prices typed item by item, shown with what they cost after the group's ratio. The server keeps it in the supplier's
// unit, so it needs the unit value; without one the dialog says so and links to confirming it.
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
  const detail = useQuery(supplierQuery(supplier.id));
  const model = useQuery(modelQuery(target.model_id));
  const group = detail.data?.groups.find((one) => one.id === target.group_id);
  const currency = supplier.settlement_currency;
  const noUnit = supplier.unit_value === undefined;
  const { model_id: _, ...item } = target;
  return (
    <ChangeDialog
      operation={`supplier-base-price:${supplier.id}:${target.group_id}:${target.upstream_name}`}
      title={t("upstream.basePrice.title", { name: target.upstream_name })}
      intro={
        <p>
          {t("upstream.basePrice.body", {
            supplier: supplier.name,
            ratio: group?.ratio ?? "—",
            currency,
          })}
        </p>
      }
      warn={noUnit ? <UnitMissing supplier={supplier} /> : undefined}
      action={t("upstream.basePrice.open")}
      ready={!noUnit && group !== undefined && model.isSuccess}
      fields={() => (
        <DataState query={model} compact>
          {(found) => (
            <BaseFields official={officialIn(found, currency)} currency={currency} ratio={group?.ratio ?? "1"} />
          )}
        </DataState>
      )}
      read={(values) => readBase(values, currency, invalid)}
      send={(choice, { reason, key }) =>
        unwrapChange(
          adminApi.POST(`${base}/base-prices`, {
            params: params(supplier, key),
            body: confirmation(item, choice, reason),
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

// The choice of where the base comes from, and the cost it makes in the group: the official price as it stands, or the
// prices as they are typed (prefilled from the official one), each shown times the ratio.
function BaseFields({ official, currency, ratio }: { official?: Schedule; currency: string; ratio: string }) {
  const { t } = useTranslation();
  const invalid = useInvalid();
  const [source, setSource] = useState<Source>(official ? "official" : "entered");
  const blank: Schedule = { v: 1, currency, rates: official?.rates ?? {} };
  const [typed, setTyped] = useState<Schedule | undefined>(official);
  const retype = (form: HTMLElement) => {
    const values = Object.fromEntries([...form.querySelectorAll("input")].map((input) => [input.name, input.value]));
    const read = readSchedule(values, blank, invalid);
    setTyped(isProblem(read) ? undefined : read);
  };
  const shown = source === "official" ? official : typed;
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
      {source === "official" && official ? (
        <Stack gap={8}>
          <Text size="sm" tone="muted">
            {t("upstream.basePrice.officialPrice")}
          </Text>
          <Rates schedule={official} />
        </Stack>
      ) : (
        <div onInput={(event) => retype(event.currentTarget)}>
          <Stack gap={8}>
            <Text size="sm" tone="muted">
              {t("upstream.basePrice.enteredHint", { currency })}
            </Text>
            <RatesFields schedule={blank} />
          </Stack>
        </div>
      )}
      <Stack gap={8}>
        <Text size="sm" tone="muted">
          {t("upstream.basePrice.after", { ratio })}
        </Text>
        {shown ? (
          <Rates schedule={scaled(shown, ratio)} />
        ) : (
          <Text size="sm" tone="faint">
            {t("upstream.basePrice.afterNone")}
          </Text>
        )}
      </Stack>
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
