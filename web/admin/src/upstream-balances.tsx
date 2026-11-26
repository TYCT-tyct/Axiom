import { can, useSession } from "@axiom/app";
import { Card, DataState, PaceBar, RowList, Text, useFormat } from "@axiom/ui";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { useAllSuppliers } from "./all-suppliers";

// A balance counted in a month, the longest the bar shows (the board draws days over 30).
const bar = 30;
// Fewer days than this is a warning, as the health score counts it.
const short = 5;
// Suppliers that are gone or going have no balance to run short of.
const gone = new Set(["removing", "removed"]);

// BalancesCard is how long each upstream's balance lasts at what the last 7 days cost (the board's 上游余额), with the
// balance as the upstream counts it. Only a supplier whose balance can be read is listed; the ones that bill later
// (direct APIs, cloud accounts) have none to run out of and are named once under the list.
export function BalancesCard() {
  const { t } = useTranslation("ops");
  const format = useFormat();
  const allowed = can(useSession(), "suppliers.view");
  const { suppliers, query } = useAllSuppliers(allowed);
  const all = suppliers.filter((one) => !gone.has(one.status));
  const readable = all.filter((one) => one.balance_native !== undefined);
  const unread = all.filter((one) => one.balance_native === undefined);
  if (!allowed) {
    return null;
  }
  return (
    <Card layout="flush" title={t("balances.title")} lede={t("balances.lede")}>
      <DataState query={query} isEmpty={() => all.length === 0} empty={<Text tone="faint">{t("balances.none")}</Text>}>
        {() => (
          <>
            <RowList
              label={t("balances.title")}
              empty={t("balances.noneReadable")}
              items={readable.map((supplier) => {
                const days = supplier.balance_days;
                return {
                  key: supplier.id,
                  title: supplier.name,
                  sub:
                    days === undefined
                      ? t("balances.noSpend")
                      : days < short
                        ? t("balances.short", { count: days })
                        : t("balances.enough", { count: days }),
                  end: (
                    <>
                      {t("balances.balance", {
                        value: format.decimal(supplier.balance_native ?? "0"),
                        unit: t(`balances.unit.${supplier.native_unit}`),
                      })}
                      {days === undefined ? null : (
                        <PaceBar
                          label={t("balances.bar", { name: supplier.name })}
                          value={days}
                          max={bar}
                          warn={days < short}
                          width={96}
                        />
                      )}
                    </>
                  ),
                  link: <Link to="/suppliers/$supplierId" params={{ supplierId: supplier.id }} />,
                };
              })}
            />
            {unread.length > 0 ? (
              <Text size="sm" tone="faint">
                {t("balances.unread", { names: unread.map((one) => one.name).join(t("balances.join")) })}
              </Text>
            ) : null}
          </>
        )}
      </DataState>
    </Card>
  );
}
