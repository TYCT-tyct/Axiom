import { type AdminSchemas, ApiError } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import {
  ActionButton,
  Card,
  Cluster,
  DataState,
  EmptyState,
  Id,
  PageHead,
  Stack,
  Table,
  Td,
  Text,
  useFormat,
} from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { percentOf } from "../approval-parts";
import { CapPolicyDialog, RepairCapDialog } from "../operations-cap-dialogs";
import { AgentLimitsDialog, PriceEnvelopeDialog, StopAgentDialog } from "../operations-exec-dialogs";
import {
  operationsAgentLimitsQuery,
  operationsCapPoliciesQuery,
  operationsPriceEnvelopesQuery,
} from "../operations-queries";
import { Facts, Head, PageBody, State, SubLine, Time, useShownAfter } from "../parts";
import { accountQuery } from "../queries";

type CapPolicy = AdminSchemas["OperationsCapPolicy"];
type Envelope = AdminSchemas["OperationsPriceEnvelopeSetting"];

const is404 = (error: unknown) => error instanceof ApiError && error.status === 404;

export default function OperationsLimitsPage() {
  const { t } = useTranslation();
  const session = useSession();
  const owner = useQuery(accountQuery(session.account));
  const caps = useQuery(operationsCapPoliciesQuery);
  const agent = useQuery(operationsAgentLimitsQuery);
  const envelopes = useQuery(operationsPriceEnvelopesQuery);
  const showAgent = useShownAfter(caps.isPending);
  const showEnvelopes = useShownAfter(caps.isPending || agent.isPending);
  const showStop = useShownAfter(caps.isPending || agent.isPending || envelopes.isPending);
  const siteId = owner.data?.site_id ?? ("site_00000000000000000000000001" as AdminSchemas["SiteId"]);
  const currency = owner.data?.balance.balance.currency ?? "USD";

  if (caps.error instanceof ApiError && caps.error.status === 403) {
    return (
      <DataState query={caps} page>
        {() => null}
      </DataState>
    );
  }

  return (
    <>
      <PageHead title={t("operations.limits.title")} lede={t("operations.limits.description")} />
      <PageBody>
        <CapPoliciesSection query={caps} siteId={siteId} currency={currency} />
        {showAgent ? <AgentLimitsSection query={agent} /> : null}
        {showEnvelopes ? <PriceEnvelopesSection query={envelopes} siteId={siteId} /> : null}
        {showStop && can(session, "operations.manage") ? <StopAgentSection /> : null}
      </PageBody>
    </>
  );
}

type CapOpen = { kind: "create" } | { kind: "edit" | "repair"; key: string } | null;
const capKey = (policy: CapPolicy) => `${policy.scope}:${policy.category}`;

function CapPoliciesSection({
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
        actions={
          <ActionButton variant="pri" size="sm" icon="plus" onClick={() => setOpen({ kind: "create" })}>
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

function LimitCeiling({ value, max }: { value: number | undefined; max: number | undefined }): ReactNode {
  const { t } = useTranslation();
  const format = useFormat();
  if (value === undefined) {
    return "—";
  }
  return (
    <span>
      {format.number(value)}
      {max === undefined ? null : (
        <>
          {" "}
          <Text as="span" tone="faint">
            {t("operations.limits.agent.ceiling", { max: format.number(max) })}
          </Text>
        </>
      )}
    </span>
  );
}

function AgentLimitsSection({ query }: { query: ReturnType<typeof useQuery<AdminSchemas["OperationsAgentLimits"]>> }) {
  const { t } = useTranslation();
  const [editing, setEditing] = useState(false);

  if (is404(query.error)) {
    return (
      <Card title={t("operations.limits.agent.title")}>
        <Text tone="muted">{t("operations.limits.unavailable")}</Text>
      </Card>
    );
  }
  return (
    <DataState query={query}>
      {(view) => (
        <>
          <Facts
            title={t("operations.limits.agent.title")}
            actions={
              <ActionButton icon="pencil" onClick={() => setEditing(true)}>
                {t("operations.limits.agent.edit")}
              </ActionButton>
            }
            rows={[
              [
                t("operations.limits.agent.requestsPerMinute"),
                <LimitCeiling key="rpm" value={view.requests_per_minute} max={view.requests_per_minute_max} />,
              ],
              [
                t("operations.limits.agent.moneyWritesPerMinute"),
                <LimitCeiling key="mwm" value={view.money_writes_per_minute} max={view.money_writes_per_minute_max} />,
              ],
              [
                t("operations.limits.agent.moneyWritesPerHour"),
                <LimitCeiling key="mwh" value={view.money_writes_per_hour} max={view.money_writes_per_hour_max} />,
              ],
              [
                t("operations.limits.agent.alertsPerHour"),
                <LimitCeiling key="aph" value={view.alerts_per_hour} max={view.alerts_per_hour_max} />,
              ],
              [
                t("operations.limits.agent.updatedAt"),
                <span key="upd">
                  <Time at={view.updated_at} />{" "}
                  <Text as="span" tone="faint">
                    {view.rev ? t("operations.limits.rev", { rev: view.rev }) : t("operations.limits.defaultRev")}
                  </Text>
                </span>,
              ],
            ]}
          />
          {editing ? <AgentLimitsDialog view={view} onClose={() => setEditing(false)} /> : null}
        </>
      )}
    </DataState>
  );
}

function PriceEnvelopesSection({
  query,
  siteId,
}: {
  query: ReturnType<typeof useQuery<AdminSchemas["OperationsPriceEnvelopeList"]>>;
  siteId: AdminSchemas["SiteId"];
}) {
  const { t } = useTranslation();
  const [editing, setEditing] = useState<string | null>(null);
  const list = query.data?.data ?? [];
  const chosen = editing && editing !== "new" ? list.find((item) => item.site_id === editing) : undefined;

  if (is404(query.error)) {
    return (
      <Card title={t("operations.limits.envelope.title")}>
        <Text tone="muted">{t("operations.limits.unavailable")}</Text>
      </Card>
    );
  }
  return (
    <>
      <Card
        title={t("operations.limits.envelope.title")}
        lede={t("operations.limits.envelope.description")}
        layout="flush"
        actions={
          <ActionButton variant="pri" size="sm" icon="plus" onClick={() => setEditing("new")}>
            {t("operations.limits.envelope.new")}
          </ActionButton>
        }
      >
        <DataState
          query={query}
          isEmpty={(data) => data.data.length === 0}
          empty={
            <EmptyState title={t("operations.limits.envelope.emptyTitle")}>
              {t("operations.limits.envelope.emptyBody")}
            </EmptyState>
          }
        >
          {(data) => <EnvelopesTable envelopes={data.data} onEdit={(env) => setEditing(env.site_id)} />}
        </DataState>
      </Card>
      {editing === "new" ? <PriceEnvelopeDialog defaultSiteId={siteId} onClose={() => setEditing(null)} /> : null}
      {editing && chosen ? (
        <PriceEnvelopeDialog
          key={chosen.site_id}
          envelope={chosen}
          defaultSiteId={siteId}
          onClose={() => setEditing(null)}
        />
      ) : null}
    </>
  );
}

function EnvelopesTable({ envelopes, onEdit }: { envelopes: Envelope[]; onEdit: (env: Envelope) => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  const percent = percentOf(format.decimal);
  return (
    <Table label={t("operations.limits.envelope.title")}>
      <Head
        cols={[
          [t("operations.limits.envelope.col.site"), { main: true }],
          [t("operations.limits.envelope.col.minMargin"), { align: "end" }],
          [t("operations.limits.envelope.col.modelDaily"), { align: "end" }],
          [t("operations.limits.envelope.col.dailyChanges"), { align: "end" }],
          [t("operations.limits.envelope.col.dailyModels"), { align: "end" }],
          t("operations.limits.envelope.col.updated"),
        ]}
        action
      />
      <tbody>
        {envelopes.map((env) => (
          <tr key={env.site_id}>
            <Td main>
              <Id value={env.site_id} />
            </Td>
            <Td align="end">{percent(env.limits.min_margin_bps)}</Td>
            <Td align="end">{percent(env.limits.model_daily_change_bps)}</Td>
            <Td align="end">{format.number(env.limits.daily_changes)}</Td>
            <Td align="end">{format.number(env.limits.daily_models)}</Td>
            <Td shrink>
              <span title={format.dateTime(new Date(env.updated_at))}>{format.dayFull(new Date(env.updated_at))}</span>
              <SubLine>{t("operations.limits.rev", { rev: env.rev })}</SubLine>
            </Td>
            <Td actions>
              <ActionButton
                size="sm"
                aria-label={t("operations.limits.envelope.editNamed", { siteId: env.site_id })}
                onClick={() => onEdit(env)}
              >
                {t("operations.limits.envelope.edit")}
              </ActionButton>
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

function StopAgentSection() {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  return (
    <>
      <Card
        title={t("operations.limits.stop.title")}
        lede={t("operations.limits.stop.description")}
        actions={
          <ActionButton variant="sec" onClick={() => setOpen(true)}>
            {t("operations.limits.stop.open")}
          </ActionButton>
        }
      >
        <Text tone="muted">{t("operations.limits.stop.cardNote")}</Text>
      </Card>
      {open ? <StopAgentDialog onClose={() => setOpen(false)} /> : null}
    </>
  );
}

export { default as messages } from "../locales/operations";
