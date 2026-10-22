import { Stat } from "@axiom/app";
import { DataState, PageHeader, Panel, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { balanceQuery } from "../queries";

export default function Balance() {
  const { t } = useTranslation();
  const format = useFormat();
  const balance = useQuery(balanceQuery);
  return (
    <>
      <PageHeader title={t("nav.balance")} description={t("balance.description")} />
      <div className="space-y-4 p-4 sm:p-6">
        <DataState query={balance}>
          {(data) => (
            <>
              <div className="grid gap-4 sm:grid-cols-2">
                <Stat
                  label={t("balance.available")}
                  value={format.money(data.available)}
                  note={t("balance.availableNote")}
                />
                <Stat
                  label={t("balance.spentThisMonth")}
                  value={format.money(data.spent_this_month)}
                  note={t("balance.monthNote")}
                />
              </div>
              <Panel title={t("balance.breakdown")}>
                <dl className="divide-y divide-b1">
                  {(
                    [
                      ["balance", data.balance],
                      ["reserved", data.reserved],
                      ["floor", data.floor],
                    ] as const
                  ).map(([name, money]) => (
                    <div key={name} className="grid grid-cols-[minmax(0,1fr)_auto] gap-x-4 gap-y-1 px-4 py-3">
                      <dt className="text-t1">{t(`balance.${name}`)}</dt>
                      <dd className="col-start-2 row-span-2 row-start-1 text-right text-t1 tabular-nums [overflow-wrap:anywhere]">
                        {format.money(money)}
                      </dd>
                      <dd className="max-w-prose text-label text-t3">{t(`balance.${name}Note`)}</dd>
                    </div>
                  ))}
                </dl>
              </Panel>
              <p className="max-w-prose text-ui text-t3">{t("balance.topUp")}</p>
              <Link to="/statement" className="text-ui text-accent underline-offset-4 hover:underline">
                {t("nav.statement")}
              </Link>
            </>
          )}
        </DataState>
      </div>
    </>
  );
}
