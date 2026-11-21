import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { can, useErrors, useSession } from "@axiom/app";
import { ActionButton, DataState, EmptyState, Field } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem, useSnapshot } from "../change";
import { Code, Facts } from "../parts";
import { publicationsQuery } from "../queries";
import { formatTargetRate } from "../targets";

type Policy = AdminSchemas["PricePolicy"];
type Input = Omit<AdminSchemas["PricePolicyInput"], "reason">;

const ratios = ["floor_multiplier", "change_ratio", "max_increase_ratio"] as const;

export function PricePolicy() {
  const { t } = useTranslation();
  const query = useQuery(publicationsQuery);
  return (
    <DataState query={query} isEmpty={({ policy }) => !policy} empty={<EmptyState title={t("pricePolicy.empty")} />}>
      {({ policy }) => (policy ? <Current policy={policy} /> : null)}
    </DataState>
  );
}

function Current({ policy }: { policy: Policy }) {
  const { t } = useTranslation();
  const manage = can(useSession(), "pricing.manage");
  const [editing, setEditing] = useState(false);
  return (
    <>
      <Facts
        title={t("pricePolicy.title")}
        actions={
          manage && policy.rev !== undefined ? (
            <ActionButton icon="pencil" onClick={() => setEditing(true)}>
              {t("pricePolicy.edit")}
            </ActionButton>
          ) : undefined
        }
        rows={[
          ...ratios.map(
            (name) => [t(`pricePolicy.fields.${name}`), <Code key={name}>{policy[name] ?? "—"}</Code>] as const,
          ),
          [
            t("pricePolicy.fields.byok_fee_bps"),
            policy.byok_fee_bps === undefined ? "—" : formatTargetRate(policy.byok_fee_bps),
          ],
        ]}
      />
      {editing ? <Edit live={policy} onClose={() => setEditing(false)} /> : null}
    </>
  );
}

function readPolicy(values: Record<string, string>, rev: number, invalid: string): Input | Problem {
  const { floor_multiplier, change_ratio, max_increase_ratio } = values;
  if (!floor_multiplier?.trim() || !change_ratio?.trim() || !max_increase_ratio?.trim()) return { problem: invalid };
  const raw = values.byok_fee_bps?.trim() ?? "";
  const fee = Number(raw);
  if (raw && (!/^\d+$/.test(raw) || fee > 10_000)) return { problem: invalid };
  return {
    rev,
    floor_multiplier: floor_multiplier.trim(),
    change_ratio: change_ratio.trim(),
    max_increase_ratio: max_increase_ratio.trim(),
    byok_fee_bps: raw ? fee : undefined,
  };
}

function Edit({ live, onClose }: { live: Policy; onClose: () => void }) {
  const { t } = useTranslation();
  const [policy, reload] = useSnapshot(live);
  const rev = policy.rev;
  if (rev === undefined) return null;
  return (
    <ChangeDialog
      operation="price-publication-policy"
      title={t("pricePolicy.edit")}
      action={t("pricePolicy.save")}
      intro={<p>{t("pricePolicy.intro")}</p>}
      fields={(error) => <PolicyFields policy={policy} error={error} />}
      read={(values) => readPolicy(values, rev, t("pricePolicy.invalid"))}
      send={(input, { key, reason }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/pricing/policy", {
            params: { header: { "Idempotency-Key": key } },
            body: { ...input, reason },
          }),
        )
      }
      done={() => <p>{t("pricePolicy.saved")}</p>}
      reload={reload}
      onClose={onClose}
    />
  );
}

function PolicyFields({ policy, error }: { policy: Policy; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      {ratios.map((name) => (
        <Field
          key={name}
          name={name}
          label={t(`pricePolicy.fields.${name}`)}
          hint={t("pricePolicy.ratioHint", { min: name === "floor_multiplier" ? 1 : 0 })}
          inputMode="decimal"
          maxLength={34}
          defaultValue={policy[name]}
          error={errors.field(error, name)}
        />
      ))}
      <Field
        name="byok_fee_bps"
        label={t("pricePolicy.fields.byok_fee_bps")}
        hint={t("pricePolicy.byokHint")}
        inputMode="numeric"
        defaultValue={policy.byok_fee_bps}
        error={errors.field(error, "byok_fee_bps")}
      />
    </>
  );
}
