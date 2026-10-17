import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { Button, DataState, EmptyState, Icon, PageHeader, Panel, Table, Td, Th, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Actions, Code, mainCell, PageBody, State } from "../parts";
import { MethodSummary } from "../pricing/describe";
import { PreviewPanel } from "../pricing/preview-panel";
import { RuleDialog } from "../pricing/rule-dialog";
import { rulesQuery } from "../queries";

type Rule = AdminSchemas["PricingRule"];

// What is being edited is the rule with this ID as the page has it now, so a change made elsewhere shows; a new rule has none.
const newRule = "new";

export default function Pricing() {
  const { t } = useTranslation();
  const rules = useQuery(rulesQuery);
  const manage = can(useSession(), "pricing.manage");
  const [editing, setEditing] = useState<string | null>(null);
  const current = rules.data?.data.find((rule) => rule.id === editing);
  return (
    <>
      <PageHeader title={t("nav.pricing")} description={t("pricing.description")} />
      <PageBody>
        {manage ? (
          <Actions>
            <Button variant="primary" size="md" onClick={() => setEditing(newRule)}>
              <Icon name="plus" />
              {t("pricing.rule.new")}
            </Button>
          </Actions>
        ) : null}
        <Panel title={t("pricing.rules.title")}>
          <DataState
            query={rules}
            isEmpty={(data) => data.data.length === 0}
            empty={<EmptyState title={t("pricing.empty.title")}>{t("pricing.empty.body")}</EmptyState>}
          >
            {(data) => <RulesTable rules={data.data} onEdit={manage ? (rule) => setEditing(rule.id) : undefined} />}
          </DataState>
        </Panel>
        <PreviewPanel />
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
      <thead>
        <tr>
          <Th>{t("pricing.rules.target")}</Th>
          <Th className="hidden sm:table-cell">{t("pricing.rules.audience")}</Th>
          <Th className="hidden md:table-cell">{t("pricing.rules.method")}</Th>
          <Th>{t("pricing.rules.state")}</Th>
          <Th className="hidden xl:table-cell">{t("pricing.rules.updated")}</Th>
          {onEdit ? <Th className="w-0" /> : null}
        </tr>
      </thead>
      <tbody>
        {rules.map((rule) => (
          <tr key={rule.id}>
            <Td className={mainCell}>
              <p className="truncate text-t1">{t(`pricing.target.${rule.target.kind}`)}</p>
              {rule.target.id ? (
                <p className="truncate text-label text-t4" title={rule.target.id}>
                  <Code>{rule.target.id}</Code>
                </p>
              ) : null}
            </Td>
            <Td className="hidden max-w-48 min-w-24 sm:table-cell">
              <p className="truncate">{t(`pricing.audience.${rule.audience.kind}`)}</p>
              {rule.audience.id ? (
                <p className="truncate text-label text-t4" title={rule.audience.id}>
                  <Code>{rule.audience.id}</Code>
                </p>
              ) : null}
            </Td>
            <Td className="hidden md:table-cell">
              <MethodSummary method={rule.method} />
            </Td>
            <Td>
              <State tone={rule.enabled ? "good" : "neutral"}>
                {rule.enabled ? t("pricing.on") : t("pricing.off")}
              </State>
            </Td>
            <Td className="hidden whitespace-nowrap xl:table-cell">{format.dateTime(new Date(rule.updated_at))}</Td>
            {onEdit ? (
              <Td className="whitespace-nowrap">
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={t("pricing.rules.edit", { target: t(`pricing.target.${rule.target.kind}`) })}
                  onClick={() => onEdit(rule)}
                >
                  <Icon name="pencil" />
                </Button>
              </Td>
            ) : null}
          </tr>
        ))}
      </tbody>
    </Table>
  );
}
