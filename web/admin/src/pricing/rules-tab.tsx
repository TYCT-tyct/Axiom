import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { Card, Cluster, DataState, DataTable, EmptyState, LinkButton, Tag, Text, useFormat } from "@axiom/ui";
import { usePrefetchQuery, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Code, useShownAfter } from "../parts";
import { listedModelsQuery, rulesQuery, tiersQuery } from "../queries";
import { MethodSummary } from "./describe";
import { PreviewPanel } from "./preview-panel";
import { RuleDialog } from "./rule-dialog";

type Rule = AdminSchemas["PricingRule"];

// What is being edited is the rule with this ID as the page has it now, so a change made elsewhere shows; a new rule has
// none.
export const newRule = "new";

// RulesTab is how a sale price is made from the reference price (adPg_pricing's rules tab): the rules, the one that
// names the most specific target winning, and a preview of what the stored rules sell the listed models for.
export function RulesTab({ editing, onEdit }: { editing: string | null; onEdit: (id: string | null) => void }) {
  const { t } = useTranslation();
  const rules = useQuery(rulesQuery);
  // The preview appears last but its lists are asked for with the rules, so it does not wait a round of its own.
  usePrefetchQuery(listedModelsQuery);
  usePrefetchQuery(tiersQuery());
  const previewShown = useShownAfter(rules.isPending);
  const manage = can(useSession(), "pricing.manage");
  const current = rules.data?.data.find((rule) => rule.id === editing);
  return (
    <>
      <Card layout="flush">
        <DataState
          query={rules}
          isEmpty={(data) => data.data.length === 0}
          empty={<EmptyState title={t("pricing.empty.title")}>{t("pricing.empty.body")}</EmptyState>}
        >
          {(data) => <RulesTable rules={data.data} onEdit={manage ? (rule) => onEdit(rule.id) : undefined} />}
        </DataState>
      </Card>
      {previewShown ? <PreviewPanel /> : null}
      {editing ? <RuleDialog rule={current} onClose={() => onEdit(null)} /> : null}
    </>
  );
}

function RulesTable({ rules, onEdit }: { rules: Rule[]; onEdit: ((rule: Rule) => void) | undefined }) {
  const { t } = useTranslation();
  const format = useFormat();
  const target = (rule: Rule) => t(`pricing.target.${rule.target.kind}`);
  return (
    <DataTable
      fixed
      label={t("pricing.rules.title")}
      rows={rules}
      rowKey={(rule) => rule.id}
      columns={[
        {
          key: "target",
          width: 240,
          header: t("pricing.rules.target"),
          cell: (rule) => (
            <Cluster gap={8}>
              {onEdit ? (
                <LinkButton label={t("pricing.rules.edit", { target: target(rule) })} onClick={() => onEdit(rule)}>
                  {target(rule)}
                </LinkButton>
              ) : (
                target(rule)
              )}
              {rule.target.id ? <Code>{rule.target.id}</Code> : null}
            </Cluster>
          ),
        },
        {
          key: "audience",
          width: 220,
          header: t("pricing.rules.audience"),
          cell: (rule) => <Audience rule={rule} />,
        },
        {
          key: "method",
          width: 200,
          header: t("pricing.rules.method"),
          cell: (rule) => <MethodSummary method={rule.method} />,
        },
        {
          key: "state",
          width: 110,
          header: t("pricing.rules.state"),
          cell: (rule) => <RuleState rule={rule} />,
        },
        {
          key: "updated",
          width: 170,
          header: t("pricing.rules.updated"),
          muted: true,
          cell: (rule) => format.dateTime(new Date(rule.updated_at)),
        },
      ]}
    />
  );
}

function Audience({ rule }: { rule: Rule }) {
  const { t } = useTranslation();
  return (
    <>
      <Text as="span">{t(`pricing.audience.${rule.audience.kind}`)}</Text>
      {rule.audience.id ? <Code>{rule.audience.id}</Code> : null}
    </>
  );
}

function RuleState({ rule }: { rule: Rule }) {
  const { t } = useTranslation();
  return (
    <Tag tone={rule.enabled ? "ok" : "neutral"} dot>
      {rule.enabled ? t("pricing.on") : t("pricing.off")}
    </Tag>
  );
}
