import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { claimSession, holdsIdentity } from "@axiom/app";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { billingPreferenceQuery } from "./queries";

export type BillingPreference = Schemas["BillingPreference"];
type BillingPreferenceName = Schemas["BillingPreferenceName"];
export type BillingPreferenceChoice = Schemas["BillingPreferenceRequest"]["preference"];

export const billingPreferenceChoices: readonly BillingPreferenceChoice[] = [
  "site",
  "subscription_first",
  "wallet_first",
  "subscription_only",
  "wallet_only",
];

/** Maps an account or site-inherited preference record to the form's selectable value. */
export function billingPreferenceChoice(pref: BillingPreference): BillingPreferenceChoice {
  return pref.source === "account" ? pref.preference : "site";
}

/** Builds select option descriptors for account charge order choices with localized site default label. */
export function billingPreferenceOptions(
  siteDefault: BillingPreferenceName,
  labelFor: (key: BillingPreferenceName) => string,
  siteLabel: (defaultLabel: string) => string,
): { value: string; label: string }[] {
  return billingPreferenceChoices.map((value) => ({
    value,
    label: value === "site" ? siteLabel(labelFor(siteDefault)) : labelFor(value),
  }));
}

export function useSaveBillingPreference(
  onSaved: (pref: BillingPreference) => void,
  onError: (error: unknown) => void,
) {
  const queryClient = useQueryClient();
  return useMutation({
    onMutate: () => claimSession(queryClient),
    mutationFn: (preference: BillingPreferenceChoice) =>
      unwrap(
        consoleApi.PUT("/api/console/v1/billing-preference", {
          body: { preference },
        }),
      ),
    onSuccess: (updated, _choice, claim) => {
      if (!claim || !holdsIdentity(queryClient, claim.identity)) return;
      queryClient.setQueryData(billingPreferenceQuery.queryKey, updated);
      onSaved(updated);
    },
    onError: (error, _choice, claim) => {
      if (!claim || !holdsIdentity(queryClient, claim.identity)) return;
      onError(error);
    },
  });
}

export function useBillingPreferenceForm(pref: BillingPreference) {
  const [choice, setChoice] = useState<BillingPreferenceChoice>(() => billingPreferenceChoice(pref));
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    setChoice(billingPreferenceChoice(pref));
  }, [pref]);

  const mutation = useSaveBillingPreference(
    (updated) => {
      setChoice(billingPreferenceChoice(updated));
      setSaved(true);
    },
    () => setSaved(false),
  );

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
