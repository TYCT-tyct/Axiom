import { LoadMore } from "@axiom/app";
import { ActionButton, Card, RowList } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Code } from "../parts";
import type { SupplierView } from "../queries";
import { type BaseTarget, ConfirmBaseDialog } from "./base-price-dialogs";
import { BuiltState } from "./parts";
import { supplierModelsQuery } from "./queries";

// The names of models on sale that have no endpoint yet: at a relay that publishes no price, the projection makes an
// endpoint only once a base is confirmed (docs/domains/suppliers.md「确认基础价」), so these are where a person starts.
// A name with an endpoint whose cost is unknown is confirmed from the endpoint's row instead.
export function Unpriced({ detail }: { detail: SupplierView }) {
  const { t } = useTranslation();
  const names = useInfiniteQuery(supplierModelsQuery(detail.supplier.id, "listed"));
  const [target, setTarget] = useState<BaseTarget | null>(null);
  const rows = (names.data?.pages.flatMap((page) => page.data) ?? []).filter((row) => !row.endpoint_id);
  const group = (id: string) => {
    const found = detail.groups.find((one) => one.id === id);
    return found?.codename ?? found?.name ?? id;
  };
  return (
    <BuiltState query={names} compact>
      {() => (
        <Card layout="rows" title={t("upstream.basePrice.waiting")} lede={t("upstream.basePrice.waitingLede")}>
          <RowList
            label={t("upstream.basePrice.waiting")}
            empty={t("upstream.basePrice.waitingNone")}
            items={rows.map((row) => ({
              key: `${row.group_id}/${row.upstream_name}`,
              title: <Code>{row.upstream_name}</Code>,
              sub: t("upstream.basePrice.waitingSub", { model: row.model_slug ?? "—", group: group(row.group_id) }),
              end: row.model_id ? (
                <ActionButton
                  size="sm"
                  aria-label={t("upstream.basePrice.openFor", { name: row.upstream_name, group: group(row.group_id) })}
                  onClick={() =>
                    setTarget({
                      group_id: row.group_id,
                      upstream_name: row.upstream_name,
                      model_id: row.model_id ?? "",
                    })
                  }
                >
                  {t("upstream.basePrice.open")}
                </ActionButton>
              ) : undefined,
            }))}
          />
          <LoadMore list={names} />
          {target ? (
            <ConfirmBaseDialog supplier={detail.supplier} target={target} onClose={() => setTarget(null)} />
          ) : null}
        </Card>
      )}
    </BuiltState>
  );
}
