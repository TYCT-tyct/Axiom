import type { AdminSchemas } from "@axiom/api";
import { TextAreaField, TextField } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { isProblem, type Problem } from "../change";
import { isPerUse, perMillion, perUnit, rateItems } from "./catalog";

type Schedule = AdminSchemas["PriceSchedule"];
type Rates = AdminSchemas["Rates"];

// The price in the unit a person reads it in: per million tokens, or per use for the items priced that way.
export function showRate(item: keyof AdminSchemas["Rates"], price: string) {
  return isPerUse(item) ? price : perMillion(price);
}

// RatesFields is the currency of a price list and the price of each billing item it prices (empty means no price for
// that item). A schedule that already has context tiers or service-tier prices keeps them; this form edits the base
// prices.
export function RatesFields({ schedule, currency }: { schedule?: Schedule; currency?: string }) {
  const { t } = useTranslation();
  return (
    <>
      <TextField
        label={t("models.price.currency")}
        hint={t("models.price.currencyHint")}
        name="currency"
        maxLength={3}
        defaultValue={currency ?? schedule?.currency}
        readOnly={schedule !== undefined}
        autoComplete="off"
        spellCheck={false}
        autoCapitalize="characters"
      />
      <fieldset className="min-w-0 space-y-3">
        <legend className="mb-1 text-ui font-medium text-t2">{t("models.price.rates")}</legend>
        <p className="text-label text-t4">{t("models.price.ratesHint")}</p>
        <div className="grid gap-x-4 gap-y-3 min-[480px]:grid-cols-2">
          {rateItems.map((item) => {
            const price = schedule?.rates[item];
            return (
              <TextField
                key={item}
                label={t(isPerUse(item) ? "models.price.perUse" : "models.price.perMillion", {
                  item: t(`models.item.${item}`),
                })}
                name={`rate_${item}`}
                inputMode="decimal"
                autoComplete="off"
                defaultValue={price === undefined ? "" : showRate(item, price)}
              />
            );
          })}
        </div>
      </fieldset>
    </>
  );
}

// What supports a price is kept as the person entered it.
function EvidenceFields() {
  const { t } = useTranslation();
  return (
    <>
      <TextAreaField
        label={t("models.price.evidence")}
        hint={t("models.price.evidenceHint")}
        name="evidence_note"
        rows={2}
      />
      <TextField
        label={t("models.price.evidenceUrl")}
        name="evidence_url"
        type="url"
        inputMode="url"
        autoComplete="off"
      />
    </>
  );
}

export function PriceFields({ schedule, currency }: { schedule?: Schedule; currency?: string }) {
  return (
    <>
      <RatesFields schedule={schedule} currency={currency} />
      <EvidenceFields />
    </>
  );
}

export type Invalid = { currency: string; rate: string; none: string; evidence: string };

// The words for what can be wrong with a price form.
export function useInvalid(): Invalid {
  const { t } = useTranslation();
  return {
    currency: t("models.price.invalidCurrency"),
    rate: t("models.price.invalidRate"),
    none: t("models.price.noRates"),
    evidence: t("models.price.noEvidence"),
  };
}

function readRate(item: keyof Rates, typed: string) {
  if (isPerUse(item)) {
    return /^(0|[1-9]\d{0,2})(\.\d{1,15})?$/.test(typed) ? typed : null;
  }
  return perUnit(typed);
}

// The schedule the form holds, as the contract has it: the base prices replaced and anything else kept. Each rate is
// decimal text all the way, so no price is a number the browser rounded.
export function readSchedule(
  values: Record<string, string>,
  existing: Schedule | undefined,
  invalid: Invalid,
): Schedule | Problem {
  const currency = existing?.currency ?? (values.currency ?? "").trim().toUpperCase();
  if (!/^[A-Z]{3}$/.test(currency)) {
    return { problem: invalid.currency };
  }
  const rates: Record<string, string> = {};
  for (const item of rateItems) {
    const typed = (values[`rate_${item}`] ?? "").trim();
    const price = typed === "" ? undefined : readRate(item, typed);
    if (price === null) {
      return { problem: invalid.rate };
    }
    if (price !== undefined) {
      rates[item] = price;
    }
  }
  if (Object.keys(rates).length === 0) {
    return { problem: invalid.none };
  }
  return { ...existing, v: 1, currency, rates: rates as Rates };
}

export function readPrice(
  values: Record<string, string>,
  existing: Schedule | undefined,
  invalid: Invalid,
): { schedule: Schedule; evidence: AdminSchemas["Evidence"] } | Problem {
  const schedule = readSchedule(values, existing, invalid);
  const note = (values.evidence_note ?? "").trim();
  const url = (values.evidence_url ?? "").trim();
  if (isProblem(schedule)) {
    return schedule;
  }
  return note ? { schedule, evidence: url ? { note, url } : { note } } : { problem: invalid.evidence };
}
