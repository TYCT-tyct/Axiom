import { type AdminSchemas, adminApi, unwrapChange, unwrapEmptyChange } from "@axiom/api";
import { can, useErrors, useSession } from "@axiom/app";
import { ActionButton, Card, Cluster, DataState, DataTable, EmptyState, Field, Tag, Text } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "../change";
import { NewButton } from "../parts";
import { rulesQuery, tiersQuery } from "../queries";
import { markupInput, markupRule, multiplierOf, percentOf } from "./markup";

type Tier = AdminSchemas["TierRef"];
type Rule = AdminSchemas["PricingRule"];
type Dialog = { kind: "rename" | "state" | "default" | "markup"; tier: Tier };

// The tiers tab: the main site's tiers, which one new accounts join, which are retired, and what each pays above the
// price for all accounts (docs/domains/accounts.md「等级」). A tier's markup is a pricing rule for it, so editing it needs
// the pricing permission as the pricing page does, and the rules are read to show it.
export function TiersTab() {
  const { t } = useTranslation();
  const session = useSession();
  const manage = can(session, "sites.manage");
  const pricing = can(session, "pricing.manage");
  const tiers = useQuery(tiersQuery());
  const rules = useQuery({ ...rulesQuery, enabled: can(session, "pricing.view") });
  const [dialog, setDialog] = useState<Dialog | null>(null);
  return (
    <>
      <Cluster justify="between">
        <Text tone="muted">{t("users.tiers.lede")}</Text>
        <NewButton permission="sites.manage" label={t("users.tiers.create")}>
          {(close) => <NameDialog onClose={close} />}
        </NewButton>
      </Cluster>
      <Card layout="flush">
        <DataState
          query={tiers}
          isEmpty={(data) => data.data.length === 0}
          empty={<EmptyState title={t("users.tiers.empty")}>{t("users.tiers.lede")}</EmptyState>}
        >
          {(data) => (
            <DataTable
              fixed
              label={t("users.tabs.tiers")}
              rows={data.data}
              rowKey={(tier) => tier.id}
              columns={[
                { key: "name", width: 200, header: t("users.tiers.name"), cell: (tier) => <TierName tier={tier} /> },
                {
                  key: "markup",
                  width: 200,
                  header: t("users.tiers.markup"),
                  cell: (tier) => <Markup rules={rules.data?.data} tier={tier} />,
                },
                {
                  key: "actions",
                  width: 360,
                  header: t("users.tiers.actions"),
                  cell: (tier) => (
                    <Cluster gap={8}>
                      {manage ? (
                        <ActionButton onClick={() => setDialog({ kind: "rename", tier })}>
                          {t("users.tiers.rename")}
                        </ActionButton>
                      ) : null}
                      {manage && !tier.default && !tier.disabled ? (
                        <ActionButton onClick={() => setDialog({ kind: "default", tier })}>
                          {t("users.tiers.makeDefault")}
                        </ActionButton>
                      ) : null}
                      {manage && !tier.default ? (
                        <ActionButton onClick={() => setDialog({ kind: "state", tier })}>
                          {tier.disabled ? t("users.tiers.enable") : t("users.tiers.disable")}
                        </ActionButton>
                      ) : null}
                      {pricing && rules.data ? (
                        <ActionButton onClick={() => setDialog({ kind: "markup", tier })}>
                          {t("users.tiers.setMarkup")}
                        </ActionButton>
                      ) : null}
                    </Cluster>
                  ),
                },
              ]}
            />
          )}
        </DataState>
      </Card>
      {dialog ? <TierDialog dialog={dialog} rules={rules.data?.data ?? []} onClose={() => setDialog(null)} /> : null}
    </>
  );
}

function TierName({ tier }: { tier: Tier }) {
  const { t } = useTranslation();
  return (
    <Cluster gap={8}>
      <Text as="span">{tier.name}</Text>
      {tier.default ? <Tag tone="ok">{t("users.tiers.default")}</Tag> : null}
      {tier.disabled ? <Tag tone="neutral">{t("users.tiers.disabled")}</Tag> : null}
    </Cluster>
  );
}

// Markup says what the tier pays beside everyone: its own multiplier, the price for all, a rule of another method, or
// nothing when the rules cannot be read.
function Markup({ rules, tier }: { rules: Rule[] | undefined; tier: Tier }) {
  const { t } = useTranslation();
  if (!rules) return <Text tone="faint">—</Text>;
  const rule = markupRule(rules, tier.id);
  if (!rule || !rule.enabled) return <Text tone="muted">{t("users.tiers.samePrice")}</Text>;
  if (rule.method.kind !== "default_multiplier" || !rule.method.multiplier) {
    return <Text tone="muted">{t("users.tiers.ownRule")}</Text>;
  }
  return <Text as="span">{t("users.tiers.pays", { percent: percentOf(rule.method.multiplier) })}</Text>;
}

function TierDialog({ dialog, rules, onClose }: { dialog: Dialog; rules: Rule[]; onClose: () => void }) {
  switch (dialog.kind) {
    case "rename":
      return <NameDialog tier={dialog.tier} onClose={onClose} />;
    case "state":
      return <StateDialog tier={dialog.tier} onClose={onClose} />;
    case "default":
      return <DefaultDialog tier={dialog.tier} onClose={onClose} />;
    case "markup":
      return <MarkupDialog tier={dialog.tier} rule={markupRule(rules, dialog.tier.id)} onClose={onClose} />;
  }
}

// NameDialog adds a tier, or renames the one given.
function NameDialog({ tier, onClose }: { tier?: Tier; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <ChangeDialog
      title={tier ? t("users.tiers.renameTitle", { name: tier.name }) : t("users.tiers.createTitle")}
      intro={tier ? undefined : <p>{t("users.tiers.createIntro")}</p>}
      operation={tier ? `tier-name:${tier.id}` : "tier-create"}
      action={tier ? t("users.tiers.rename") : t("users.tiers.create")}
      fields={(error) => (
        <Field
          label={t("users.tiers.name")}
          name="name"
          defaultValue={tier?.name}
          maxLength={100}
          error={errors.field(error, "name")}
        />
      )}
      read={(values): { name: string } | Problem => {
        const name = (values.name ?? "").trim();
        return name ? { name } : { problem: t("users.tiers.nameRequired") };
      }}
      send={({ name }, { reason, key }) =>
        tier
          ? unwrapChange(
              adminApi.PATCH("/api/admin/v1/tiers/{tier_id}", {
                params: { path: { tier_id: tier.id }, header: { "Idempotency-Key": key } },
                body: { name, reason },
              }),
            )
          : unwrapChange(
              adminApi.POST("/api/admin/v1/tiers", { params: { header: { "Idempotency-Key": key } }, body: { name, reason } }),
            )
      }
      toast={(saved) => t("users.tiers.saved", { name: saved.name })}
      done={() => null}
      onClose={onClose}
    />
  );
}

// StateDialog retires a tier, or brings a retired one back; the default tier is never retired, so it is not offered.
function StateDialog({ tier, onClose }: { tier: Tier; onClose: () => void }) {
  const { t } = useTranslation();
  const disable = !tier.disabled;
  const send = (disabled: boolean, reason: string, key: string) =>
    unwrapChange(
      adminApi.PATCH("/api/admin/v1/tiers/{tier_id}", {
        params: { path: { tier_id: tier.id }, header: { "Idempotency-Key": key } },
        body: { disabled, reason },
      }),
    );
  return (
    <ChangeDialog
      title={t(disable ? "users.tiers.disableTitle" : "users.tiers.enableTitle", { name: tier.name })}
      intro={<p>{t(disable ? "users.tiers.disableIntro" : "users.tiers.enableIntro")}</p>}
      operation={`tier-state:${tier.id}`}
      action={disable ? t("users.tiers.disable") : t("users.tiers.enable")}
      danger={disable}
      read={() => ({ disabled: disable })}
      send={({ disabled }, { reason, key }) => send(disabled, reason, key)}
      toast={(saved) => t("users.tiers.saved", { name: saved.name })}
      undo={(_, { reason, key }) => send(!disable, reason, key)}
      done={() => null}
      onClose={onClose}
    />
  );
}

function DefaultDialog({ tier, onClose }: { tier: Tier; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      title={t("users.tiers.defaultTitle", { name: tier.name })}
      intro={<p>{t("users.tiers.defaultIntro")}</p>}
      operation={`tier-default:${tier.id}`}
      action={t("users.tiers.makeDefault")}
      read={() => ({})}
      send={(_, { reason, key }) =>
        unwrapEmptyChange(
          adminApi.PUT("/api/admin/v1/tiers/{tier_id}/default", {
            params: { path: { tier_id: tier.id }, header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      toast={() => t("users.tiers.defaultDone", { name: tier.name })}
      done={() => null}
      onClose={onClose}
    />
  );
}

// MarkupDialog sets the share the tier pays above the price for all accounts, as the tier's rule for every model: it
// changes the rule there is, or adds one. A rule of another method is replaced only once the person sees that it is.
function MarkupDialog({ tier, rule, onClose }: { tier: Tier; rule: Rule | undefined; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const current = rule?.enabled && rule.method.kind === "default_multiplier" ? rule.method.multiplier : undefined;
  return (
    <ChangeDialog
      title={t("users.tiers.markupTitle", { name: tier.name })}
      intro={<p>{t("users.tiers.markupIntro")}</p>}
      warn={rule && rule.method.kind !== "default_multiplier" ? t("users.tiers.replacesRule") : undefined}
      operation={`tier-markup:${tier.id}`}
      action={t("users.tiers.setMarkup")}
      fields={(error) => (
        <Field
          label={t("users.tiers.percent")}
          hint={t("users.tiers.percentHint")}
          name="percent"
          inputMode="decimal"
          defaultValue={current ? percentOf(current) : ""}
          error={errors.field(error, "rule/method/multiplier")}
        />
      )}
      read={(values): AdminSchemas["PricingRuleInput"] | Problem => {
        const multiplier = multiplierOf(values.percent ?? "");
        return multiplier ? markupInput(tier.id, multiplier) : { problem: t("users.tiers.percentInvalid") };
      }}
      send={(input, { reason, key }) =>
        rule
          ? unwrapChange(
              adminApi.PUT("/api/admin/v1/pricing/rules/{rule_id}", {
                params: { path: { rule_id: rule.id }, header: { "Idempotency-Key": key } },
                body: { rev: rule.rev, rule: input, reason },
              }),
            )
          : unwrapChange(
              adminApi.POST("/api/admin/v1/pricing/rules", {
                params: { header: { "Idempotency-Key": key } },
                body: { rule: input, reason },
              }),
            )
      }
      toast={(saved) =>
        t("users.tiers.markupDone", {
          name: tier.name,
          percent: saved.method.multiplier ? percentOf(saved.method.multiplier) : "0",
        })
      }
      done={() => null}
      onClose={onClose}
    />
  );
}
