import { adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { ActionButton, DataState, EmptyState, Field, PageHead, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "../change";
import en from "../locales/en/retention";
import zh from "../locales/zh/retention";
import { Facts, PageBody } from "../parts";
import { logRetentionQuery } from "../policy-queries";

export const messages = { zh: { logRetention: zh }, en: { logRetention: en } };

export default function Retention() {
  const { t } = useTranslation();
  const query = useQuery(logRetentionQuery);
  return (
    <>
      <PageHead title={t("nav.retention")} lede={t("logRetention.description")} />
      <PageBody>
        <DataState
          query={query}
          isEmpty={(view) => view.hot_days === undefined}
          empty={<EmptyState title={t("logRetention.empty")} />}
        >
          {(view) => <Current days={view.hot_days ?? 0} />}
        </DataState>
      </PageBody>
    </>
  );
}

function Current({ days }: { days: number }) {
  const { t } = useTranslation();
  const format = useFormat();
  const [editing, setEditing] = useState(false);
  return (
    <>
      <Facts
        title={t("logRetention.hot")}
        actions={
          <ActionButton icon="pencil" onClick={() => setEditing(true)}>
            {t("logRetention.edit")}
          </ActionButton>
        }
        rows={[[t("logRetention.days"), days === 0 ? t("logRetention.keepAll") : format.number(days)]]}
      />
      {editing ? <Edit days={days} onClose={() => setEditing(false)} /> : null}
    </>
  );
}

function Edit({ days, onClose }: { days: number; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <ChangeDialog
      operation="log-retention"
      title={t("logRetention.edit")}
      action={t("logRetention.save")}
      fields={(error) => (
        <Field
          name="hot_days"
          label={t("logRetention.days")}
          hint={t("logRetention.hint")}
          inputMode="numeric"
          defaultValue={days}
          error={errors.field(error, "hot_days")}
        />
      )}
      read={({ hot_days }) => {
        const value = Number(hot_days);
        return /^\d+$/.test(hot_days?.trim() ?? "") && value <= 3650 ? value : { problem: t("logRetention.invalid") };
      }}
      send={(hot_days, { key, reason }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/retention", {
            params: { header: { "Idempotency-Key": key } },
            body: { hot_days, reason },
          }),
        )
      }
      done={() => <p>{t("logRetention.saved")}</p>}
      onClose={onClose}
    />
  );
}
