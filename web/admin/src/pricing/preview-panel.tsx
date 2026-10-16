import type { AdminSchemas } from "@axiom/api";
import { readForm } from "@axiom/app";
import { Button, CheckField, DataState, EmptyState, Panel, SelectField, TextField } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { listedModelsQuery, previewLimit, tiersQuery } from "../queries";
import { type PreviewRequest, PreviewResult } from "./preview";

type Model = AdminSchemas["ModelSummary"];

// How many of the listed models start out chosen: enough to see the pattern, few enough to read.
const startChosen = 10;

// ModelChoice lists the models on sale to pick the ones to price; a preview takes at most previewLimit of them.
function ModelChoice({ models }: { models: Model[] }) {
  const { t } = useTranslation();
  const [filter, setFilter] = useState("");
  const shown = models.filter((model) => model.slug.includes(filter.trim().toLowerCase()));
  return (
    <fieldset className="min-w-0 space-y-2">
      <legend className="mb-1 text-ui font-medium text-t2">{t("pricing.preview.models")}</legend>
      <TextField
        label={t("pricing.preview.modelFilter")}
        hint={t("pricing.preview.modelsHint", { max: previewLimit })}
        value={filter}
        onChange={(event) => setFilter(event.target.value)}
        autoComplete="off"
        spellCheck={false}
      />
      <div className="max-h-56 space-y-2 overflow-y-auto rounded-btn border border-b2 bg-s1 p-3">
        {models.map((model, index) => (
          <CheckField
            key={model.id}
            className={shown.includes(model) ? undefined : "hidden"}
            label={model.slug}
            name={`model_${model.id}`}
            defaultChecked={index < startChosen}
          />
        ))}
        {shown.length === 0 ? <p className="text-t3">{t("pricing.preview.noModels")}</p> : null}
      </div>
    </fieldset>
  );
}

function requestOf(values: Record<string, string>, models: Model[]): PreviewRequest | null {
  const ids = models.filter((model) => values[`model_${model.id}`] === "on").slice(0, previewLimit);
  if (ids.length === 0) {
    return null;
  }
  const account = (values.account ?? "").trim();
  return {
    model_ids: ids.map((model) => model.id),
    tier_id: account || !values.tier_id ? undefined : (values.tier_id as AdminSchemas["TierId"]),
    account_id: account ? (account as AdminSchemas["AccountId"]) : undefined,
  };
}

// PreviewPanel answers "what would these models sell for, to this audience, with what margin" under the stored rules.
export function PreviewPanel() {
  const { t } = useTranslation();
  const models = useQuery(listedModelsQuery);
  const tiers = useQuery(tiersQuery);
  const [request, setRequest] = useState<PreviewRequest | null>(null);
  const [none, setNone] = useState(false);
  return (
    <Panel title={t("pricing.preview.title")}>
      <div className="space-y-4 p-4">
        <p className="max-w-prose text-t2">{t("pricing.preview.intro")}</p>
        <DataState
          query={models}
          compact
          isEmpty={(data) => data.data.length === 0}
          empty={<EmptyState compact title={t("pricing.preview.noListed")} />}
        >
          {(data) => (
            <form
              noValidate
              className="space-y-4"
              onSubmit={(event) => {
                event.preventDefault();
                const next = requestOf(readForm(event.currentTarget), data.data);
                setNone(next === null);
                setRequest(next);
              }}
            >
              <div className="grid gap-4 sm:grid-cols-2">
                <SelectField label={t("pricing.preview.tier")} name="tier_id" defaultValue="">
                  <option value="">{t("pricing.preview.defaultTier")}</option>
                  {tiers.data?.data.map((tier) => (
                    <option key={tier.id} value={tier.id}>
                      {tier.name}
                    </option>
                  ))}
                </SelectField>
                <TextField
                  label={t("pricing.preview.account")}
                  hint={t("pricing.preview.accountHint")}
                  name="account"
                  autoComplete="off"
                  spellCheck={false}
                />
              </div>
              <ModelChoice models={data.data} />
              {none ? (
                <p role="alert" className="text-bad">
                  {t("pricing.preview.pick")}
                </p>
              ) : null}
              <Button type="submit" variant="primary" size="md">
                {t("pricing.preview.run")}
              </Button>
            </form>
          )}
        </DataState>
        <PreviewResult request={request} />
      </div>
    </Panel>
  );
}
