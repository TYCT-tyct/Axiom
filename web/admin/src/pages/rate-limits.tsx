import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { can, readForm, useErrors, useSession } from "@axiom/app";
import {
  ActionButton,
  Card,
  Cluster,
  DataState,
  EmptyState,
  Field,
  ForbiddenState,
  FormError,
  Grid,
  PageHead,
  Select,
  Stack,
  useFormat,
} from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "../change";
import en from "../locales/en/rate-limits";
import zh from "../locales/zh/rate-limits";
import { Code, Facts, PageBody } from "../parts";
import { type RateTarget, rateLimitQuery, rateTargets } from "../policy-queries";

export const messages = { zh: { rateLimits: zh }, en: { rateLimits: en } };

export default function RateLimits() {
  const { t } = useTranslation();
  const allowed = can(useSession(), "sites.manage");
  return (
    <>
      <PageHead title={t("nav.rateLimits")} lede={t("rateLimits.description")} />
      <PageBody>{allowed ? <Lookup /> : <ForbiddenState />}</PageBody>
    </>
  );
}

const ids = {
  tier: /^tier_[0-9a-hjkmnp-tv-z]{26}$/,
  account: /^acct_[0-9a-hjkmnp-tv-z]{26}$/,
  key: /^key_[0-9a-hjkmnp-tv-z]{26}$/,
};

function Lookup() {
  const { t } = useTranslation();
  const [selected, setSelected] = useState<RateTarget>();
  const [invalid, setInvalid] = useState(false);
  return (
    <>
      <Card>
        <form
          onSubmit={(event) => {
            event.preventDefault();
            const { target, id } = readForm(event.currentTarget);
            const scope = rateTargets.find((value) => value === target);
            const trimmed = id?.trim();
            const valid = scope && trimmed && ids[scope].test(trimmed);
            setInvalid(!valid);
            setSelected(valid ? { target: scope, id: trimmed } : undefined);
          }}
        >
          <Stack>
            <Grid min="16rem">
              <Select
                name="target"
                label={t("rateLimits.target")}
                options={rateTargets.map((value) => ({ value, label: t(`rateLimits.targets.${value}`) }))}
              />
              <Field
                name="id"
                label={t("rateLimits.id")}
                hint={t("rateLimits.idHint")}
                autoComplete="off"
                spellCheck={false}
                maxLength={31}
              />
            </Grid>
            {invalid ? <FormError>{t("rateLimits.invalidId")}</FormError> : null}
            <Cluster>
              <ActionButton variant="pri" type="submit">
                {t("rateLimits.lookup")}
              </ActionButton>
            </Cluster>
          </Stack>
        </form>
      </Card>
      {selected ? (
        <Allowance key={`${selected.target}/${selected.id}`} path={selected} />
      ) : (
        <EmptyState title={t("rateLimits.empty")} />
      )}
    </>
  );
}

function Allowance({ path }: { path: RateTarget }) {
  const { t } = useTranslation();
  const query = useQuery(rateLimitQuery(path));
  return (
    <DataState
      query={query}
      isEmpty={(view) => view.effective_requests === undefined}
      empty={<EmptyState title={t("rateLimits.unavailable")} />}
    >
      {(view) => <Current path={path} view={view} />}
    </DataState>
  );
}

function Current({ path, view }: { path: RateTarget; view: AdminSchemas["RateLimitView"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  const [editing, setEditing] = useState(false);
  const count = (value: number | null | undefined) =>
    value == null ? t("rateLimits.inherits") : value === 0 ? t("rateLimits.unlimited") : format.number(value);
  return (
    <>
      <Facts
        title={t("rateLimits.allowance")}
        actions={
          <ActionButton icon="pencil" onClick={() => setEditing(true)}>
            {t("rateLimits.edit")}
          </ActionButton>
        }
        rows={[
          [t("rateLimits.id"), <Code key="id">{path.id}</Code>],
          [t("rateLimits.own"), count(view.requests)],
          [t("rateLimits.effective"), count(view.effective_requests)],
        ]}
      />
      {editing ? <Edit path={path} own={view.requests} onClose={() => setEditing(false)} /> : null}
    </>
  );
}

function Edit({ path, own, onClose }: { path: RateTarget; own?: number | null; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <ChangeDialog
      operation={`rate-limit:${path.target}:${path.id}`}
      title={t("rateLimits.edit")}
      action={t("rateLimits.save")}
      intro={<Code>{path.id}</Code>}
      fields={(error) => (
        <Field
          name="requests"
          label={t("rateLimits.own")}
          hint={t("rateLimits.hint")}
          inputMode="numeric"
          defaultValue={own ?? ""}
          error={errors.field(error, "requests")}
        />
      )}
      read={({ requests }) => {
        const raw = requests?.trim() ?? "";
        if (!raw) return null;
        const value = Number(raw);
        return /^\d+$/.test(raw) && value <= 1_000_000_000 ? value : { problem: t("rateLimits.invalid") };
      }}
      send={(requests, { key, reason }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/rate-limits/{target}/{id}", {
            params: { path, header: { "Idempotency-Key": key } },
            body: { requests, reason },
          }),
        )
      }
      done={() => <p>{t("rateLimits.saved")}</p>}
      onClose={onClose}
    />
  );
}
