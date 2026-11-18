import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { billingPreferenceQuery } from "./queries";

export type BillingPreference = Schemas["BillingPreference"];
type BillingPreferenceName = Schemas["BillingPreferenceName"];
export type BillingPreferenceChoice = Schemas["BillingPreferenceRequest"]["preference"];

const BILLING_PREFERENCE_KEYS: readonly BillingPreferenceName[] = [
  "subscription_first",
  "wallet_first",
  "subscription_only",
  "wallet_only",
];

/** Maps an account or site-inherited preference record to the form's selectable value. */
function billingPreferenceChoice(pref: BillingPreference): BillingPreferenceChoice {
  return pref.source === "account" ? pref.preference : "site";
}

/** Builds select option descriptors for account charge order choices with localized site default label. */
export function billingPreferenceOptions(
  siteDefault: BillingPreferenceName,
  labelFor: (key: BillingPreferenceName) => string,
  siteLabel: (defaultLabel: string) => string,
): { value: string; label: string }[] {
  return [
    { value: "site", label: siteLabel(labelFor(siteDefault)) },
    ...BILLING_PREFERENCE_KEYS.map((key) => ({ value: key, label: labelFor(key) })),
  ];
}

/** Shared charge-order preference state and PUT mutation with cache synchronization. */
export function useBillingPreferenceForm(pref: BillingPreference) {
  const queryClient = useQueryClient();
  const [choice, setChoice] = useState<BillingPreferenceChoice>(() => billingPreferenceChoice(pref));
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    setChoice(billingPreferenceChoice(pref));
  }, [pref]);

  const mutation = useMutation({
    mutationFn: (preference: BillingPreferenceChoice) =>
      unwrap(
        consoleApi.PUT("/api/console/v1/billing-preference", {
          body: { preference },
        }),
      ),
    onSuccess: (updated) => {
      queryClient.setQueryData(billingPreferenceQuery.queryKey, updated);
      setChoice(billingPreferenceChoice(updated));
      setSaved(true);
    },
    onError: () => {
      setSaved(false);
    },
  });

  return {
    choice,
    selectChoice: (next: BillingPreferenceChoice) => {
      setSaved(false);
      setChoice(next);
    },
    saved,
    dismissSaved: () => setSaved(false),
    submit: () => {
      setSaved(false);
      mutation.mutate(choice);
    },
    pending: mutation.isPending,
    error: mutation.error,
  };
}
