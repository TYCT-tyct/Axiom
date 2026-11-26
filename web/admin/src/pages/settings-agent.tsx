import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { ActionButton, Card, Text } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import operations from "../locales/operations";
import settings from "../locales/settings";
import { CapPoliciesCard } from "../operations-caps-card";
import { CredentialsCard } from "../operations-credentials-card";
import { StopAgentDialog } from "../operations-exec-dialogs";
import { AgentLimitsRows } from "../operations-limit-rows";
import {
  operationsAgentLimitsQuery,
  operationsCapPoliciesQuery,
  operationsPriceEnvelopesQuery,
} from "../operations-queries";
import { OptionalView } from "../setting-rows";
import { SiteContentPage } from "../site-content-page";

export default function AgentSettings() {
  const { t } = useTranslation();
  return (
    <SiteContentPage
      title={t("shell.nav.agent")}
      description={t("settings.pages.agent.lede")}
      permission="operations.manage"
      actions={
        <ActionButton asChild>
          <Link to="/approvals">{t("settings.pages.agent.approvals")}</Link>
        </ActionButton>
      }
    >
      {(siteId, currency) => <Sections siteId={siteId} currency={currency} />}
    </SiteContentPage>
  );
}

function Sections({ siteId, currency }: { siteId: AdminSchemas["SiteId"]; currency: string }) {
  const session = useSession();
  const caps = useQuery(operationsCapPoliciesQuery);
  const limits = useQuery(operationsAgentLimitsQuery);
  const envelopes = useQuery(operationsPriceEnvelopesQuery);
  return (
    <>
      {can(session, "operations.manage") ? <StopCard /> : null}
      <CredentialsCard />
      <CapPoliciesCard query={caps} siteId={siteId} currency={currency} />
      <OptionalView query={limits}>
        {(limitsView) => (
          <OptionalView query={envelopes}>
            {(envelopesView) => (
              <AgentLimitsRows
                siteId={siteId}
                limits={limitsView}
                envelope={envelopesView?.data.find((entry) => entry.site_id === siteId)}
                envelopesKnown={envelopesView !== undefined}
              />
            )}
          </OptionalView>
        )}
      </OptionalView>
    </>
  );
}

// The emergency stop: every credential at once, with the agent's proposals left as they are. It cannot be taken back, so
// the name is typed first and there is no undo.
function StopCard() {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  return (
    <>
      <Card
        title={t("operations.limits.stop.title")}
        lede={t("operations.limits.stop.description")}
        scope={{ label: t("scope.platform"), tip: t("scope.platformTip") }}
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

export const messages = {
  zh: { ...operations.zh, ...settings.zh },
  en: { ...operations.en, ...settings.en },
};
