import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import {
  CopyButton,
  DataState,
  DrawerSection,
  FactList,
  Id,
  Mono,
  type PanelSteps,
  SidePanel,
  Text,
  TextLink,
  useFormat,
} from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { useEventLabel } from "./audit-events";
import { Json } from "./parts";
import { auditEntryQuery } from "./queries";

type Entry = AdminSchemas["AuditEntry"];

const missing = "—";

// One audit record, opened from the list (adP_audit in admin_pages_c.js): who, when, which action on what and why, then
// the detail the module wrote with it. The list's row has everything but the detail, which is read as text by its own
// request so that large integers in it stay exact.
export function AuditDrawer({ entry, steps, onClose }: { entry: Entry; steps?: PanelSteps; onClose: () => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  const label = useEventLabel()(entry.action);
  const calls = can(useSession(), "calls.search");
  const detail = useQuery(auditEntryQuery(entry.id ?? ""));
  return (
    <SidePanel
      drawer
      title={label.text}
      subtitle={entry.at ? format.dateTime(new Date(entry.at)) : undefined}
      steps={steps}
      onClose={onClose}
      footer={entry.id ? <CopyButton text={entry.id} label={t("audit.copyId")} /> : undefined}
    >
      <DrawerSection title={t("audit.drawer.what")}>
        <FactList
          rows={[
            [t("audit.actionId"), <Mono key="action">{entry.action ?? missing}</Mono>],
            [t("audit.module"), entry.module ? <Mono>{entry.module}</Mono> : missing],
            [t("audit.col.actor"), <Who key="actor" kind={entry.actor_kind} id={entry.actor_id} />],
            [t("audit.col.target"), <Who key="target" kind={entry.target_kind} id={entry.target_id} />],
            [t("audit.site"), entry.site_id ? <Id value={entry.site_id} /> : missing],
            [t("audit.account"), entry.account_id ? <Id value={entry.account_id} /> : missing],
            [t("audit.col.request"), <Request key="request" entry={entry} link={calls} />],
          ]}
        />
      </DrawerSection>
      <DrawerSection title={t("audit.reason")}>
        <Text tone={entry.reason ? undefined : "faint"}>{entry.reason || t("audit.noReason")}</Text>
      </DrawerSection>
      <DrawerSection title={t("audit.detail")}>
        <DataState query={detail}>
          {(data) =>
            data.detail === undefined ? (
              <Text tone="faint">{t("audit.detailUnavailable")}</Text>
            ) : (
              <Json value={data.detail} label={t("audit.detail")} />
            )
          }
        </DataState>
      </DrawerSection>
    </SidePanel>
  );
}

// A kind and the ID of a thing of that kind: the actor of a change, or what it was made to.
function Who({ kind, id }: { kind?: string; id?: string }) {
  if (!id) return missing;
  return (
    <>
      <Id value={id} />
      {kind ? (
        <Text as="div" size="sm" tone="faint">
          {kind}
        </Text>
      ) : null}
    </>
  );
}

// The request link crosses into the calls page, which a session may read without being allowed to; it is a link only
// when it is, and otherwise the ID is still shown.
function Request({ entry, link }: { entry: Entry; link: boolean }) {
  const { t } = useTranslation();
  if (!entry.request_id) return missing;
  if (!link) return <Id value={entry.request_id} />;
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
