import type { AdminSchemas } from "@axiom/api";
import { readForm } from "@axiom/app";
import {
  ActionButton,
  Card,
  CheckField,
  DataState,
  EmptyState,
  Field,
  Fieldset,
  Grid,
  InlineError,
  Select,
  Stack,
  Text,
} from "@axiom/ui";
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
    <Fieldset legend={t("pricing.preview.models")}>
      <Field
        label={t("pricing.preview.modelFilter")}
        hint={t("pricing.preview.modelsHint", { max: previewLimit })}
        value={filter}
        onChange={(event) => setFilter(event.target.value)}
        autoComplete="off"
        spellCheck={false}
      />
      <Grid min="16rem" gap={8}>
        {models.map((model, index) => (
          <CheckField
            key={model.id}
            hidden={!shown.includes(model)}
            label={model.slug}
            name={`model_${model.id}`}
            defaultChecked={index < startChosen}
          />
        ))}
      </Grid>
      {shown.length === 0 ? <Text tone="faint">{t("pricing.preview.noModels")}</Text> : null}
    </Fieldset>
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
  const tiers = useQuery(tiersQuery());
  const [request, setRequest] = useState<PreviewRequest | null>(null);
  const [none, setNone] = useState(false);
  return (
    <Card title={t("pricing.preview.title")}>
      <Stack>
        <Text tone="muted">{t("pricing.preview.intro")}</Text>
        <DataState
          query={models}
          compact
          isEmpty={(data) => data.data.length === 0}
          empty={<EmptyState compact title={t("pricing.preview.noListed")} />}
        >
          {(data) => (
            <form
              noValidate
              onSubmit={(event) => {
                event.preventDefault();
                const next = requestOf(readForm(event.currentTarget), data.data);
                setNone(next === null);
                setRequest(next);
              }}
            >
              <Stack>
                <Grid min="16rem">
                  <Select label={t("pricing.preview.tier")} name="tier_id" defaultValue="">
                    <option value="">{t("pricing.preview.defaultTier")}</option>
                    {tiers.data?.data.map((tier) => (
                      <option key={tier.id} value={tier.id}>
                        {tier.name}
                      </option>
                    ))}
                  </Select>
                  <Field
                    label={t("pricing.preview.account")}
                    hint={t("pricing.preview.accountHint")}
                    name="account"
                    autoComplete="off"
                    spellCheck={false}
                  />
                </Grid>
                <ModelChoice models={data.data} />
                {none ? <InlineError>{t("pricing.preview.pick")}</InlineError> : null}
                <div>
                  <ActionButton type="submit" variant="pri">
                    {t("pricing.preview.run")}
                  </ActionButton>
                </div>
              </Stack>
            </form>
          )}
        </DataState>
        <PreviewResult request={request} />
      </Stack>
    </Card>
  );
}
