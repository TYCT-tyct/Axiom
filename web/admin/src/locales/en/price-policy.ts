export default {
  title: "Pricing policy",
  empty: "No pricing policy available",
  edit: "Edit pricing policy",
  save: "Save pricing policy",
  intro:
    "The policy affects sale prices and fees for bring-your-own-key calls. Price candidates are recalculated after saving.",
  invalid: "Enter the three ratios. Omit the BYOK fee or enter a whole number from 0 to 10,000.",
  ratioHint: "A ratio from {{min}} to 10 with up to 15 decimal places.",
  byokHint: "In basis points. 500 is 5%. Leave blank to keep the current fee.",
  saved: "Pricing policy saved.",
  fields: {
    floor_multiplier: "Price floor multiplier",
    change_ratio: "Change ratio",
    max_increase_ratio: "Maximum increase ratio",
    byok_fee_bps: "BYOK fee",
  },
};
