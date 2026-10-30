import { type AdminSchemas, ApiError, adminApi, unwrapChange } from "@axiom/api";
import { Button, DataState, EmptyState, Icon, Panel, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, useSnapshot } from "./change";
import { MerchantFields } from "./merchant-form";
import { Facts, NewButton, State } from "./parts";
import { formatPercent, readMerchant } from "./payment-read";
import { merchantsQuery } from "./queries";

type Merchant = AdminSchemas["Merchant"];

export function MerchantsCard({ siteId, currency }: { siteId: AdminSchemas["SiteId"]; currency: string }) {
  const { t } = useTranslation();
  const query = useQuery(merchantsQuery(siteId));
  if (query.error instanceof ApiError && query.error.status === 404) {
    return <MerchantsUnavailable />;
  }
  return (
    <Panel
      title={t("settings.payments.merchants")}
      action={
        <NewButton permission="sites.manage" label={t("settings.payments.add")}>
          {(close) => <MerchantDialog siteId={siteId} currency={currency} onClose={close} />}
        </NewButton>
      }
    >
      <DataState
        query={query}
        isEmpty={(list) => (list.data ?? []).length === 0}
        empty={
          <EmptyState compact title={t("settings.payments.empty")}>
            <p>{t("settings.payments.emptyBody")}</p>
          </EmptyState>
        }
      >
        {(list) => (
          <div className="divide-y divide-b1">
            {(list.data ?? []).map((merchant) => (
              <MerchantRow key={merchant.id} merchant={merchant} siteId={siteId} currency={currency} />
            ))}
          </div>
        )}
      </DataState>
    </Panel>
  );
}

// MerchantsUnavailable is what an older node, which has no merchant list operation, shows: the rest of the page works.
function MerchantsUnavailable() {
  const { t } = useTranslation();
  return (
    <Facts
      title={t("settings.payments.merchants")}
      rows={[[t("settings.payments.merchantsUnavailableLabel"), t("settings.payments.merchantsUnavailable")]]}
    />
  );
}

function MerchantRow({
  merchant,
  siteId,
  currency,
}: {
  merchant: Merchant;
  siteId: AdminSchemas["SiteId"];
  currency: string;
}) {
  const [read, reload] = useSnapshot(merchant);
  const [edit, setEdit] = useState(false);
  const [test, setTest] = useState(false);
  const [disable, setDisable] = useState(false);
  return (
    <div className="space-y-2 px-4 py-3">
      <MerchantHeader
        merchant={merchant}
        onTest={() => setTest(true)}
        onEdit={() => {
          // Take the current revision when the dialog opens, not the one from when this row first rendered.
          reload();
          setEdit(true);
        }}
        onDisable={() => setDisable(true)}
      />
      <MerchantDetails merchant={merchant} />
      {edit ? (
        <MerchantDialog
          siteId={siteId}
          currency={currency}
          merchant={read}
          reload={reload}
          onClose={() => setEdit(false)}
        />
      ) : null}
      {test ? <TestDialog merchant={merchant} onClose={() => setTest(false)} /> : null}
      {disable ? <DisableDialog merchant={merchant} onClose={() => setDisable(false)} /> : null}
    </div>
  );
}

function MerchantHeader({
  merchant,
  onTest,
  onEdit,
  onDisable,
}: {
  merchant: Merchant;
  onTest: () => void;
  onEdit: () => void;
  onDisable: () => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-wrap items-center justify-between gap-2">
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <span className="font-medium text-t1 [overflow-wrap:anywhere]">{merchant.name ?? merchant.id}</span>
        <State tone={merchant.enabled ? "good" : "neutral"}>
          {t(merchant.enabled ? "settings.payments.enabled" : "settings.payments.disabled")}
        </State>
        <span className="font-mono text-label text-t3">{merchant.kind}</span>
        <span className="text-label text-t3">{merchant.currency}</span>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button size="sm" onClick={onTest}>
          {t("settings.payments.test")}
        </Button>
        <Button size="sm" onClick={onEdit}>
          <Icon name="pencil" />
          {t("settings.payments.editMerchant")}
        </Button>
        <Button size="sm" variant="danger" disabled={!merchant.enabled} onClick={onDisable}>
          {t("settings.payments.disable")}
        </Button>
      </div>
    </div>
  );
}

function MerchantDetails({ merchant }: { merchant: Merchant }) {
  const { t } = useTranslation();
  const format = useFormat();
  const money = (value: AdminSchemas["Money"] | undefined) =>
    value ? format.money(value) : t("settings.payments.none");
  return (
    <dl className="grid gap-x-4 gap-y-1 text-label sm:grid-cols-[10rem_minmax(0,1fr)]">
      <dt className="text-t4">{t("settings.payments.methods")}</dt>
      <dd className="min-w-0 space-y-1 text-t2">
        {(merchant.methods ?? []).length === 0 ? (
          <span>{t("settings.payments.noMethods")}</span>
        ) : (
          (merchant.methods ?? []).map((method) => <MethodLine key={method.method} method={method} money={money} />)
        )}
      </dd>
      <dt className="text-t4">{t("settings.payments.ttl")}</dt>
      <dd className="text-t2">
        {t("settings.payments.ttlValue", { seconds: format.number(merchant.ttl_seconds ?? 0) })}
      </dd>
      <dt className="text-t4">{t("settings.payments.refunds")}</dt>
      <dd className="text-t2">
        {t("settings.payments.refundValue", {
          automatic: merchant.refund_enabled ? t("settings.payments.yes") : t("settings.payments.no"),
          user: merchant.user_refund ? t("settings.payments.yes") : t("settings.payments.no"),
        })}
      </dd>
      <dt className="text-t4">{t("settings.payments.revision")}</dt>
      <dd className="font-mono text-t2">{merchant.rev ?? "—"}</dd>
    </dl>
  );
}

function MethodLine({
  method,
  money,
}: {
  method: AdminSchemas["MerchantMethod"];
  money: (value: AdminSchemas["Money"] | undefined) => string;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-wrap gap-x-3 gap-y-0.5">
      <span className="font-mono">{method.method}</span>
      <span className="text-t4">{t("settings.payments.upstreamValue", { code: method.upstream })}</span>
      <span>
        {t("settings.payments.feeValue", {
          percent: formatPercent(method.fee_ppm ?? 0),
          fixed: money(method.fee_fixed),
        })}
      </span>
      <span className="text-t3">
        {t("settings.payments.methodLimits", {
          min: money(method.single_min),
          max: money(method.single_max),
          daily: t("settings.payments.daily", { amount: money(method.daily_limit) }),
        })}
      </span>
      {method.enabled === false ? <span className="text-t4">{t("settings.payments.methodOff")}</span> : null}
    </div>
  );
}

function MerchantDialog({
  siteId,
  currency,
  merchant,
  reload,
  onClose,
}: {
  siteId: AdminSchemas["SiteId"];
  currency: string;
  merchant?: Merchant;
  reload?: () => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={merchant ? `merchant:${merchant.id}` : `merchant-new:${siteId}`}
      title={t(merchant ? "settings.payments.editMerchant" : "settings.payments.add")}
      intro={<p>{t("settings.payments.merchantIntro")}</p>}
      action={t("settings.payments.saveMerchant")}
      reload={reload}
      fields={(error) => <MerchantFields merchant={merchant} currency={currency} error={error} />}
      read={(values) =>
        readMerchant(values, merchant, {
          name: t("settings.payments.invalidName"),
          number: t("settings.payments.invalidNumber"),
          settings: t("settings.payments.invalidSettings"),
          secret: t("settings.payments.invalidSecret"),
          amount: t("settings.payments.invalidAmount", { currency: values.currency ?? currency }),
          fee: t("settings.payments.invalidFee"),
          range: t("settings.payments.invalidRange"),
          methods: t("settings.payments.invalidMethods"),
        })
      }
      send={(input, { reason, key }) =>
        unwrapChange(
          merchant
            ? adminApi.PUT("/api/admin/v1/payments/merchants/{merchant_id}", {
                params: { path: { merchant_id: merchant.id ?? "" }, header: { "Idempotency-Key": key } },
                body: { merchant: input, reason },
              })
            : adminApi.POST("/api/admin/v1/payments/merchants", {
                params: { header: { "Idempotency-Key": key } },
                body: { site_id: siteId, merchant: input, reason },
              }),
        )
      }
      done={(saved) => <p>{t("settings.payments.merchantSaved", { name: saved.name ?? "" })}</p>}
      onClose={onClose}
    />
  );
}

function TestDialog({ merchant, onClose }: { merchant: Merchant; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`merchant-test:${merchant.id}`}
      title={t("settings.payments.testTitle", { name: merchant.name ?? merchant.id })}
      intro={<p>{t("settings.payments.testIntro")}</p>}
      action={t("settings.payments.runTest")}
      verdict
      read={() => ({})}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/payments/merchants/{merchant_id}/tests", {
            params: { path: { merchant_id: merchant.id ?? "" }, header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      done={(result) => <p>{t(`settings.payments.status.${result.status ?? "failed"}`)}</p>}
      onClose={onClose}
    />
  );
}

function DisableDialog({ merchant, onClose }: { merchant: Merchant; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`merchant-disable:${merchant.id}`}
      title={t("settings.payments.disable")}
      intro={<p>{t("settings.payments.disableIntro", { name: merchant.name ?? merchant.id })}</p>}
      action={t("settings.payments.disable")}
      danger
      read={() => ({})}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/payments/merchants/{merchant_id}/disable", {
            params: { path: { merchant_id: merchant.id ?? "" }, header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      done={() => <p>{t("settings.payments.disabledNow")}</p>}
      onClose={onClose}
    />
  );
}
