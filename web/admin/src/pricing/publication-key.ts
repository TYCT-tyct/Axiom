import type { AdminSchemas } from "@axiom/api";

// Confirmation recovery survives a newer candidate while remaining isolated to the same model and audience.
export function publicationKey(price: AdminSchemas["PricePublication"]) {
  const model = price.model_id ?? "";
  if (!price.audience || price.audience.kind === "all") {
    return model;
  }
  return `${model}/${price.audience.kind}/${price.audience.id ?? ""}`;
}
