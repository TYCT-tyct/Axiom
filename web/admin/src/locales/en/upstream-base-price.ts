import type { Messages } from "@axiom/ui";
import type zh from "../zh/upstream-base-price";

export default {
  basePrice: {
    open: "Confirm base price",
    openFor: "Confirm the base price of {{name}} (group {{group}})",
    title: "Confirm the base price of {{name}}",
    body: "{{supplier}} publishes no price for this model, so its cost is unknown and it is not routed. Once you confirm a base price, the cost is the base × the group ratio {{ratio}}, in {{currency}}. A price the supplier publishes later always wins.",
    source: "Where the base comes from",
    official: "Catalog official price",
    entered: "Enter per item",
    officialPrice: "The catalog's official price",
    noOfficial: "The catalog has no {{currency}} official price for this model. Enter the prices item by item.",
    enteredHint: "In {{currency}}. An item left empty has no price.",
    after: "Cost after the group ratio {{ratio}}",
    afterNone: "Enter a price to see the cost after the group ratio.",
    unitMissingBody: "A base price is stored in the supplier's own unit, which needs the unit value.",
    unitAction: "Confirm the unit value first",
    refused: {
      conflict:
        "This supplier's unit value is not confirmed yet, or it is not a relay. Confirm the unit value in settings first.",
      unprocessable:
        "This base price cannot be stored: the name is not mapped to a catalog model yet, every price is 0, or a price is above the unit price limit after conversion.",
    },
    done: "Base price confirmed. The sync runs now: its next round prices the model at the base × the group ratio, and the endpoint then joins routing.",
    clear: "Clear base price",
    clearTitle: "Clear the base price of {{name}}",
    clearBody: "After the next sync round, this model's cost is unknown again and its endpoint leaves routing.",
    cleared: "Base price cleared. It takes effect at the next sync round.",
    clearedNone: "This model had no confirmed base price. Nothing changed.",
    waiting: "Waiting for a base price",
    waitingLede:
      "These models are on sale, but the supplier publishes no price for them. They get an endpoint once you confirm a base price.",
    waitingNone: "No model is waiting for a base price.",
    waitingSub: "{{model}}, group {{group}}",
  },
} satisfies Messages<typeof zh>;
