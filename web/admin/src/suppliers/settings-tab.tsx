import { can, useSession } from "@axiom/app";
import { ActionButton, Card, SettingRow, Text, useFormat } from "@axiom/ui";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Facts, Time } from "../parts";
import type { SupplierView } from "../queries";
import { RemoveDialog, ReplaceLoginDialog, UnitValueDialog } from "./dialogs";
import { NativeBalance, SupplierState, UnitValue } from "./parts";
import { ReplaceCloudDialog } from "./supplier-dialogs";
import { modeOf } from "./upstream";

type Open = "login" | "unit" | "replace" | "remove";

// 设置 (adSupParts' acct): how Axiom reaches the channel and what its costs go by, and removing it; then what is known of
// it. A relay signs in and has its unit valued, a direct API goes by the provider's price list, a cloud platform has
// its credential.
export default function SettingsTab({ detail }: { detail: SupplierView }) {
  const { t } = useTranslation();
  const { supplier } = detail;
  const manage = can(useSession(), "suppliers.manage") && supplier.status !== "removed";
  const removable = manage && supplier.status !== "removing";
  const [open, setOpen] = useState<Open | null>(null);
  const button = (to: Open, label: string, danger = false) =>
    manage ? (
      <ActionButton size="sm" variant={danger ? "dan" : undefined} onClick={() => setOpen(to)}>
        {label}
      </ActionButton>
    ) : null;
  const close = () => setOpen(null);
  return (
    <>
      <Card layout="rows" title={t("upstream.settings.account")}>
        <Ways detail={detail} button={button} />
        {removable ? (
          <SettingRow title={t("upstream.settings.remove")} detail={t("upstream.settings.removeNote")}>
            {button("remove", t("suppliers.remove.open"), true)}
          </SettingRow>
        ) : null}
      </Card>
      <About detail={detail} />
      {open === "login" ? <ReplaceLoginDialog supplier={supplier} onClose={close} /> : null}
      {open === "unit" ? <UnitValueDialog supplier={supplier} onClose={close} /> : null}
      {open === "replace" ? <ReplaceCloudDialog supplier={supplier} onClose={close} /> : null}
      {open === "remove" ? <RemoveDialog supplier={supplier} onClose={close} /> : null}
    </>
  );
}

type Button = (to: Open, label: string, danger?: boolean) => React.ReactNode;

function Ways({ detail, button }: { detail: SupplierView; button: Button }) {
  const { t } = useTranslation();
  const { supplier } = detail;
  const regions = detail.credentials.filter((credential) => credential.region && credential.status !== "erased");
  switch (modeOf(supplier)) {
    case "direct":
      return (
        <>
          <SettingRow
            title={t("upstream.settings.keys")}
            detail={t("upstream.settings.keysNote", {
              count: detail.credentials.filter((c) => c.status !== "erased").length,
            })}
          >
            {null}
          </SettingRow>
          <SettingRow title={t("upstream.settings.costSource")} detail={t("upstream.settings.costList")}>
            {null}
          </SettingRow>
        </>
      );
    case "cloud":
      return (
        <>
          <SettingRow
            title={t("upstream.settings.credential")}
            detail={t("upstream.settings.credentialNote", {
              account: supplier.cloud_account?.client_email ?? supplier.cloud_account?.project_id ?? supplier.kind,
              count: regions.length,
            })}
          >
            {button("replace", t("upstream.settings.replace"))}
          </SettingRow>
          <SettingRow title={t("upstream.settings.costSource")} detail={t("upstream.settings.costBill")}>
            {null}
          </SettingRow>
        </>
      );
    default:
      return (
        <>
          <SettingRow
            title={t("upstream.settings.login")}
            detail={t("upstream.settings.loginNote", { account: supplier.upstream_account })}
          >
            {button("login", t("suppliers.login.open"))}
          </SettingRow>
          <SettingRow
            title={t("upstream.settings.unit")}
            detail={
              supplier.unit_value === undefined ? t("upstream.settings.unitMissing") : <UnitValue supplier={supplier} />
            }
          >
            {button("unit", t("suppliers.unit.open"))}
          </SettingRow>
        </>
      );
  }
}

function About({ detail }: { detail: SupplierView }) {
  const { t } = useTranslation();
  const format = useFormat();
  const { supplier } = detail;
  const relay = modeOf(supplier) === "relay";
  return (
    <Facts
      title={t("upstream.settings.facts")}
      rows={[
        [t("suppliers.col.status"), <SupplierState key="state" supplier={supplier} />],
        [t("suppliers.detail.origin"), supplier.origin],
        ...(relay ? [[t("suppliers.detail.account"), supplier.upstream_account] as [string, string]] : []),
        [
          t("suppliers.detail.kind"),
          [supplier.kind, supplier.generation, supplier.detected_version].filter(Boolean).join(" · "),
        ],
        ...(relay
          ? ([
              [t("suppliers.col.balance"), <NativeBalance key="balance" supplier={supplier} />],
              [t("suppliers.detail.observed"), <Time key="observed" at={supplier.balance_observed_at} />],
              [
                t("suppliers.detail.lowLine"),
                supplier.low_balance_threshold ? format.decimal(supplier.low_balance_threshold) : "—",
              ],
              [t("suppliers.col.unit"), <UnitValue key="unit" supplier={supplier} />],
            ] as [string, React.ReactNode][])
          : []),
        [
          t("suppliers.detail.currency"),
          <Text key="currency" as="span">
            {supplier.settlement_currency}
          </Text>,
        ],
      ]}
    />
  );
}
