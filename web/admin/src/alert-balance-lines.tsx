import type { AdminSchemas } from "@axiom/api";
import { ActionButton, SettingRow, Text, useFormat } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Code } from "./parts";
import { suppliersQuery } from "./queries";
import { SettingsCard } from "./setting-rows";

// BalanceLinesCard shows each supplier's low-balance alert line (api/admin.yaml LowBalanceThreshold), naming a supplier by
// the first page of suppliers and by its ID past it; the lines are edited with the rules and channels.
export function BalanceLinesCard({
  lines,
  onEdit,
}: {
  lines: AdminSchemas["LowBalanceThreshold"][];
  onEdit: () => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  const suppliers = useInfiniteQuery(suppliersQuery());
  const names = new Map((suppliers.data?.pages ?? []).flatMap((page) => page.data ?? []).map((s) => [s.id, s.name]));
  return (
    <SettingsCard
      scope="platform"
      title={t("alertConfig.balanceLines")}
      actions={
        <ActionButton size="sm" icon="pencil" onClick={onEdit}>
          {t("alertConfig.editLines")}
        </ActionButton>
      }
    >
      {lines.map((line) => (
        <SettingRow
          key={line.supplier_id}
          title={names.get(line.supplier_id) ?? line.supplier_id}
          detail={t("alertConfig.balanceLine")}
        >
          <Code>{format.decimal(line.threshold)}</Code>
        </SettingRow>
      ))}
      <Text size="sm" tone="faint">
        {t("alertConfig.balanceDefault")}
      </Text>
    </SettingsCard>
  );
}
