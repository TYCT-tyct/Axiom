import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { ActionButton, Card, DataState, Icon, PageHead, Text, TextLink } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Actions, Code, Facts, Json, PageBody, Time } from "../parts";
import { auditEntryQuery } from "../queries";

type Entry = AdminSchemas["AuditEntry"];

const missing = "—";

export default function AuditEntry() {
  const { t } = useTranslation();
  const { entryId } = useParams({ from: "/app/audit/$entryId" });
  const entry = useQuery(auditEntryQuery(entryId));
  return (
    <DataState query={entry} page>
      {(data) => (
        <>
          <PageHead title={data.action ?? t("audit.detailTitle")} lede={t("audit.detailDescription")} />
          <PageBody>
            <Actions>
              <ActionButton asChild>
                <Link to="/audit">
                  <Icon name="left" />
                  {t("audit.back")}
                </Link>
              </ActionButton>
            </Actions>
            <Facts
              title={t("audit.detailTitle")}
              rows={[
                [t("audit.col.at"), <Time key="at" at={data.at} />],
                [t("audit.col.action"), <Code key="action">{data.action ?? "—"}</Code>],
                [t("audit.filters.module"), <Code key="module">{data.module ?? "—"}</Code>],
                [t("audit.col.actor"), <Actor key="actor" entry={data} />],
                [t("audit.col.target"), <Target key="target" entry={data} />],
                [t("audit.filters.site"), <Code key="site">{data.site_id ?? "—"}</Code>],
                [t("audit.filters.account"), <Code key="account">{data.account_id ?? "—"}</Code>],
                [t("audit.filters.request"), <Request key="request" entry={data} />],
                [t("audit.reason"), data.reason ?? "—"],
              ]}
            />
            <Card title={t("audit.detail")}>
              {data.detail === undefined ? (
                <Text tone="faint">{t("audit.detailUnavailable")}</Text>
              ) : (
                <Json value={data.detail} label={t("audit.detail")} />
              )}
            </Card>
          </PageBody>
        </>
      )}
    </DataState>
  );
}

function Actor({ entry }: { entry: Entry }) {
  return (
    <>
      <Code>{entry.actor_id ?? "—"}</Code>
      <Text as="div" size="sm" tone="faint">
        {entry.actor_kind ?? ""}
      </Text>
    </>
  );
}

function Target({ entry }: { entry: Entry }) {
  return (
    <>
      <Code>{entry.target_id ?? "—"}</Code>
      <Text as="div" size="sm" tone="faint">
        {entry.target_kind ?? ""}
      </Text>
    </>
  );
}

// The request link crosses into the calls pages, which a session may read without being allowed to; it is a link only
// when both permissions are held, and otherwise the ID is still shown.
function Request({ entry }: { entry: Entry }) {
  const { t } = useTranslation();
  const calls = can(useSession(), "calls.search");
  if (!entry.request_id) {
    return <span>{missing}</span>;
  }
  if (!calls) {
    return <Code>{entry.request_id}</Code>;
  }
  return (
    <TextLink>
      <Link to="/calls" search={{ request_id: entry.request_id, ...callWindow(entry.at) }}>
        {t("audit.openCalls")}
      </Link>
    </TextLink>
  );
}

// The call list defaults to the last 24 hours, which would hide an older linked call. Carry the widest window the API
// accepts, ending one day after the audit entry: a call is written before the entry that refers to it.
export function callWindow(at: string | undefined) {
  const time = at ? Date.parse(at) : Number.NaN;
  if (Number.isNaN(time)) {
    return {};
  }
  return {
    since: new Date(time - 30 * 24 * 60 * 60 * 1000).toISOString(),
    until: new Date(time + 24 * 60 * 60 * 1000).toISOString(),
  };
}
