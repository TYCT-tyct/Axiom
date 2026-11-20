import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { ActionButton, Card, DataState, EmptyState, PageHead, Table, Td, Text, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Actions, Code, Head, PageBody, State } from "../parts";
import { MethodSummary } from "../pricing/describe";
import { PreviewPanel } from "../pricing/preview-panel";
import { Publications } from "../pricing/publications";
import { RuleDialog } from "../pricing/rule-dialog";
import { publicationsQuery, rulesQuery } from "../queries";

type Rule = AdminSchemas["PricingRule"];

// What is being edited is the rule with this ID as the page has it now, so a change made elsewhere shows; a new rule has none.
const newRule = "new";

export default function Pricing() {
  const { t } = useTranslation();
  const rules = useQuery(rulesQuery);
  const publications = useQuery(publicationsQuery);
  const manage = can(useSession(), "pricing.manage");
  const [editing, setEditing] = useState<string | null>(null);
  const current = rules.data?.data.find((rule) => rule.id === editing);
  return (
    <>
      <PageHead title={t("nav.pricing")} lede={t("pricing.description")} />
      <PageBody>
        {manage ? (
          <Actions>
            <ActionButton variant="pri" icon="plus" onClick={() => setEditing(newRule)}>
              {t("pricing.rule.new")}
            </ActionButton>
          </Actions>
        ) : null}
        <Card layout="flush" title={t("pricing.rules.title")}>
          <DataState
            query={rules}
            isEmpty={(data) => data.data.length === 0}
            empty={<EmptyState title={t("pricing.empty.title")}>{t("pricing.empty.body")}</EmptyState>}
          >
            {(data) => <RulesTable rules={data.data} onEdit={manage ? (rule) => setEditing(rule.id) : undefined} />}
          </DataState>
        </Card>
        {/* Each section appears once the one above it has its answer: one that grew when its answer came pushed the
            sections below it down the screen (#747). Both lists are asked for at once. */}
        {rules.isPending ? null : <Publications />}
        {rules.isPending || publications.isPending ? null : <PreviewPanel />}
      </PageBody>
      {editing ? <RuleDialog rule={current} onClose={() => setEditing(null)} /> : null}
    </>
  );
}

function RulesTable({ rules, onEdit }: { rules: Rule[]; onEdit: ((rule: Rule) => void) | undefined }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <Table label={t("pricing.rules.title")}>
      <Head
        cols={[
          [t("pricing.rules.target"), { main: true }],
          t("pricing.rules.audience"),
          t("pricing.rules.method"),
          t("pricing.rules.state"),
          t("pricing.rules.updated"),
        ]}
        action={onEdit !== undefined}
      />
      <tbody>
        {rules.map((rule) => (
          <tr key={rule.id}>
            <Td main>
              <Text>{t(`pricing.target.${rule.target.kind}`)}</Text>
              {rule.target.id ? (
                <Text size="sm" tone="faint">
                  <Code>{rule.target.id}</Code>
                </Text>
              ) : null}
            </Td>
            <Td>
              <Text>{t(`pricing.audience.${rule.audience.kind}`)}</Text>
              {rule.audience.id ? (
                <Text size="sm" tone="faint">
                  <Code>{rule.audience.id}</Code>
                </Text>
              ) : null}
            </Td>
            <Td>
              <MethodSummary method={rule.method} />
            </Td>
            <Td>
              <State tone={rule.enabled ? "good" : "neutral"}>
                {rule.enabled ? t("pricing.on") : t("pricing.off")}
              </State>
            </Td>
            <Td>{format.dateTime(new Date(rule.updated_at))}</Td>
            {onEdit ? (
              <Td actions>
                <ActionButton
                  variant="gho"
                  size="sm"
                  icon="pencil"
                  aria-label={t("pricing.rules.edit", { target: t(`pricing.target.${rule.target.kind}`) })}
                  onClick={() => onEdit(rule)}
                />
              </Td>
            ) : null}
          </tr>
        ))}
      </tbody>
    </Table>
  );
}
