import { type AdminSchemas, ApiError } from "@axiom/api";
import { ActionButton, Card, Cluster, DataState, EmptyState, Id, Stack, Table, Td, Text, useFormat } from "@axiom/ui";
import type { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { CapPolicyDialog, RepairCapDialog } from "./operations-cap-dialogs";
import { Head, State, SubLine } from "./parts";

type CapPolicy = AdminSchemas["OperationsCapPolicy"];

const is404 = (error: unknown) => error instanceof ApiError && error.status === 404;

type CapOpen = { kind: "create" } | { kind: "edit" | "repair"; key: string } | null;
const capKey = (policy: CapPolicy) => `${policy.scope}:${policy.category}`;

export function CapPoliciesCard({
  query,
  siteId,
  currency,
}: {
  query: ReturnType<typeof useQuery<AdminSchemas["OperationsCapPolicyList"]>>;
  siteId: AdminSchemas["SiteId"];
  currency: string;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState<CapOpen>(null);
  const list = query.data?.data ?? [];
  const chosen = open && open.kind !== "create" ? list.find((p) => capKey(p) === open.key) : undefined;

  if (is404(query.error)) {
    return (
      <Card title={t("operations.limits.cap.title")}>
        <Text tone="muted">{t("operations.limits.unavailable")}</Text>
      </Card>
    );
  }
  return (
    <>
      <Card
        title={t("operations.limits.cap.title")}
        lede={t("operations.limits.cap.description")}
        layout="flush"
        scope={{ label: t("scope.platform"), tip: t("scope.platformTip") }}
        actions={
          <ActionButton size="sm" icon="plus" onClick={() => setOpen({ kind: "create" })}>
            {t("operations.limits.cap.new")}
          </ActionButton>
        }
      >
        <DataState
          query={query}
          isEmpty={(data) => data.data.length === 0}
          empty={
            <EmptyState title={t("operations.limits.cap.emptyTitle")}>
              {t("operations.limits.cap.emptyBody")}
            </EmptyState>
          }
        >
          {(data) => <CapTable policies={data.data} onOpen={setOpen} />}
        </DataState>
      </Card>
      {open?.kind === "create" ? (
        <CapPolicyDialog siteId={siteId} currency={currency} onClose={() => setOpen(null)} />
      ) : null}
      {open?.kind === "edit" && chosen ? (
        <CapPolicyDialog
          key={capKey(chosen)}
          policy={chosen}
          siteId={siteId}
          currency={currency}
          onClose={() => setOpen(null)}
        />
      ) : null}
      {open?.kind === "repair" && chosen ? <RepairCapDialog policy={chosen} onClose={() => setOpen(null)} /> : null}
    </>
  );
}

function CapTable({ policies, onOpen }: { policies: CapPolicy[]; onOpen: (open: CapOpen) => void }) {
  const { t } = useTranslation();
  return (
    <Table label={t("operations.limits.cap.title")}>
      <Head
        cols={[
          [t("operations.limits.cap.col.scope"), { main: true }],
          [t("operations.limits.cap.col.autonomous"), { main: true }],
          [t("operations.limits.cap.col.hard"), { main: true }],
          t("operations.limits.cap.col.state"),
        ]}
        action
      />
      <tbody>
        {policies.map((policy) => (
          <CapRow key={capKey(policy)} policy={policy} onOpen={onOpen} />
        ))}
      </tbody>
    </Table>
  );
}

function CapRow({ policy, onOpen }: { policy: CapPolicy; onOpen: (open: CapOpen) => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  const money = (amount: string) => format.money({ amount, currency: policy.currency });
  const scopeText = policy.scope === "platform" ? t("operations.limits.cap.platform") : policy.scope;
  const categoryText = t(`operations.limits.cap.category.${policy.category}`);
  const { limits } = policy;
  return (
    <tr>
      <Td main>
        <Stack gap={4}>
          <Text as="span">{categoryText}</Text>
          {policy.scope === "platform" ? <SubLine>{scopeText}</SubLine> : <Id value={policy.scope} />}
        </Stack>
      </Td>
      <Td main>
        <Text as="div">
          {t("operations.limits.cap.autoSummary", {
            single: money(limits.single_max),
            daily: money(limits.daily_max),
            weekly: money(limits.weekly_max),
          })}
        </Text>
        <SubLine>
          {t("operations.limits.cap.autoDetail", {
            object: money(limits.object_daily_max),
            count: format.number(limits.daily_count_max),
          })}
        </SubLine>
      </Td>
      <Td main>
        <Text as="div">
          {t("operations.limits.cap.hardSummary", {
            single: money(limits.hard_single_max),
            daily: money(limits.hard_daily_max),
            weekly: money(limits.hard_weekly_max),
          })}
        </Text>
      </Td>
      <Td shrink>
        <State tone={policy.paused_at ? "warn" : "good"}>
          {t(policy.paused_at ? "operations.limits.cap.paused" : "operations.limits.cap.normal")}
        </State>
        <SubLine>
          {policy.paused_at
            ? t("operations.limits.cap.pausedSince", { at: format.dateTimeMinute(new Date(policy.paused_at)) })
            : t("operations.limits.rev", { rev: policy.rev })}
        </SubLine>
      </Td>
      <Td actions>
        <CapActions policy={policy} scopeText={scopeText} categoryText={categoryText} onOpen={onOpen} />
      </Td>
    </tr>
  );
}

function CapActions({
  policy,
  scopeText,
  categoryText,
  onOpen,
}: {
  policy: CapPolicy;
  scopeText: string;
  categoryText: string;
  onOpen: (open: CapOpen) => void;
}) {
  const { t } = useTranslation();
  return (
    <Cluster justify="end">
      <ActionButton
        size="sm"
        aria-label={t("operations.limits.cap.editNamed", { scope: scopeText, category: categoryText })}
        onClick={() => onOpen({ kind: "edit", key: capKey(policy) })}
      >
        {t("operations.limits.cap.edit")}
      </ActionButton>
      <ActionButton
        size="sm"
        aria-label={t("operations.limits.cap.repairNamed", { scope: scopeText, category: categoryText })}
        onClick={() => onOpen({ kind: "repair", key: capKey(policy) })}
      >
        {t("operations.limits.cap.repair")}
      </ActionButton>
    </Cluster>
  );
}
