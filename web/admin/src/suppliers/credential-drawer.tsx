import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import {
  ActionButton,
  Cluster,
  DrawerFacts,
  type PanelSteps,
  QuotaMeter,
  SettingRow,
  SidePanel,
  Switch,
  Text,
  TextLink,
  useFormat,
} from "@axiom/ui";
import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Code } from "../parts";
import { checkText, EnableDialog, matesOf, RemoveCredentialDialog, TestDialog } from "./credential-dialogs";
import { CredentialState } from "./credentials";
import { CredentialLimitsDialog } from "./dialogs";
import { ReplaceCloudDialog } from "./supplier-dialogs";
import { credentialName, modeOf } from "./upstream";

type Credential = AdminSchemas["SupplierCredential"];
type Supplier = AdminSchemas["Supplier"];
type Open = "enable" | "test" | "remove" | "limits" | "replace";

// A credential's drawer (adP_upk): the 接请求 switch with where requests go when it is off, how much of its ceilings it
// holds, what it is (a relay's key, a pasted key on a direct API, a cloud region), and its changes. A key the supplier
// refused says why, in red.
export function CredentialDrawer({
  credential,
  supplier,
  line,
  all,
  steps,
  onClose,
}: {
  credential: Credential;
  supplier: Supplier | undefined;
  line: string;
  all: readonly Credential[];
  steps?: PanelSteps;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const manage = can(useSession(), "suppliers.manage") && credential.status !== "erased";
  const [open, setOpen] = useState<Open | null>(null);
  const mates = matesOf(credential, all).length;
  const mode = supplier ? modeOf(supplier) : "relay";
  const close = () => setOpen(null);
  return (
    <SidePanel
      drawer
      title={credentialName(credential)}
      subtitle={[supplier?.name, line, t(`upstream.mode.${mode}`)].filter(Boolean).join("，")}
      steps={steps}
      onClose={onClose}
      footer={manage ? <Actions credential={credential} supplier={supplier} onOpen={setOpen} /> : undefined}
    >
      {credential.status === "rejected" && credential.last_check ? (
        <Text tone="bad">{t("upstream.credential.rejected", { cause: checkText(t, credential.last_check) })}</Text>
      ) : null}
      <SettingRow title={t("upstream.credential.takes")} detail={takes(t, credential, line, mates)}>
        <Switch
          label={t("upstream.credential.takes")}
          checked={credential.status !== "disabled"}
          disabled={!manage}
          onChange={() => setOpen("enable")}
        />
      </SettingRow>
      <Meters credential={credential} />
      <Facts credential={credential} supplier={supplier} line={line} mates={mates} />
      <Opened open={open} credential={credential} supplier={supplier} line={line} mates={mates} onClose={close} />
    </SidePanel>
  );
}

function Opened({
  open,
  credential,
  supplier,
  line,
  mates,
  onClose,
}: {
  open: Open | null;
  credential: Credential;
  supplier: Supplier | undefined;
  line: string;
  mates: number;
  onClose: () => void;
}) {
  switch (open) {
    case "enable":
      return <EnableDialog credential={credential} line={line} mates={mates} onClose={onClose} />;
    case "test":
      return <TestDialog credential={credential} onClose={onClose} />;
    case "remove":
      return <RemoveCredentialDialog credential={credential} line={line} mates={mates} onClose={onClose} />;
    case "limits":
      return supplier ? <CredentialLimitsDialog supplier={supplier} credential={credential} onClose={onClose} /> : null;
    case "replace":
      return supplier ? <ReplaceCloudDialog supplier={supplier} onClose={onClose} /> : null;
    default:
      return null;
  }
}

type T = TFunction<"admin">;

function takes(t: T, credential: Credential, line: string, mates: number) {
  if (credential.status === "disabled") return t("upstream.credential.takesOff");
  return mates > 0
    ? t("upstream.credential.takesMates", { count: mates })
    : t("upstream.credential.takesOnly", { line });
}

function Actions({
  credential,
  supplier,
  onOpen,
}: {
  credential: Credential;
  supplier: Supplier | undefined;
  onOpen: (open: Open) => void;
}) {
  const { t } = useTranslation();
  const cloud = supplier && modeOf(supplier) === "cloud";
  return (
    <Cluster justify="end">
      {credential.key_source === "pasted" ? (
        <ActionButton variant="gho" onClick={() => onOpen("remove")}>
          {t("upstream.credential.delete")}
        </ActionButton>
      ) : null}
      {cloud ? (
        <ActionButton variant="gho" onClick={() => onOpen("replace")}>
          {t("upstream.credential.replace")}
        </ActionButton>
      ) : null}
      {credential.status !== "disabled" ? (
        <ActionButton icon="status" onClick={() => onOpen("test")}>
          {t("upstream.credential.test")}
        </ActionButton>
      ) : null}
      <ActionButton variant="pri" onClick={() => onOpen("limits")}>
        {t("upstream.credential.limits")}
      </ActionButton>
    </Cluster>
  );
}

function Meters({ credential }: { credential: Credential }) {
  const { t } = useTranslation();
  const used = credential.in_flight ?? 0;
  const minute = credential.requests_last_minute ?? 0;
  const rpm = credential.requests_per_minute;
  return (
    <>
      <QuotaMeter
        label={t("upstream.credential.concurrency")}
        used={used}
        of={credential.max_concurrency}
        share={used / credential.max_concurrency}
        over={used >= credential.max_concurrency}
        note={used >= credential.max_concurrency ? t("upstream.credential.full") : undefined}
      />
      <QuotaMeter
        label={t("upstream.credential.minute")}
        used={minute}
        of={rpm ?? t("upstream.credential.noCeiling")}
        share={rpm ? minute / rpm : 0}
        over={rpm !== undefined && minute >= rpm}
      />
    </>
  );
}

function Facts({
  credential,
  supplier,
  line,
  mates,
}: {
  credential: Credential;
  supplier: Supplier | undefined;
  line: string;
  mates: number;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  const own = ownFacts(t, supplier, credential, mates);
  return (
    <DrawerFacts
      rows={[
        [
          t("upstream.credential.channel"),
          supplier ? (
            <TextLink key="c">
              <Link to="/suppliers/$supplierId" params={{ supplierId: supplier.id }}>
                {supplier.name}
              </Link>
            </TextLink>
          ) : (
            "—"
          ),
        ],
        [t("upstream.credential.line"), line],
        ...own,
        [t("upstream.keys.col.status"), <CredentialState key="s" credential={credential} />],
        ...(credential.key_source
          ? [
              [t("upstream.credential.sourceLabel"), t(`upstream.credential.source.${credential.key_source}`)] as [
                string,
                string,
              ],
            ]
          : []),
        ...(credential.created_at
          ? [[t("upstream.keys.col.added"), format.dateTimeMinute(new Date(credential.created_at))] as [string, string]]
          : []),
      ]}
    />
  );
}

type Row = [string, ReactNode];

// What only a direct API's key or a cloud region has: the address and format the key calls, or the platform, project
// and region.
function ownFacts(t: T, supplier: Supplier | undefined, credential: Credential, mates: number): Row[] {
  const mode = supplier ? modeOf(supplier) : "relay";
  if (mode === "direct") {
    const format: Row[] = supplier?.api_format
      ? [[t("upstream.credential.format"), t(`upstream.addDialog.formats.${supplier.api_format}`)]]
      : [];
    return [
      [t("upstream.credential.origin"), <Code key="o">{supplier?.origin}</Code>],
      ...format,
      [t("upstream.credential.mates"), t("upstream.credential.matesCount", { count: mates + 1 })],
    ];
  }
  if (mode !== "cloud") return [];
  const project = supplier?.cloud_account?.project_id;
  return [
    [t("upstream.credential.cloud"), supplier?.kind ?? ""],
    ...(project ? ([[t("upstream.credential.project"), <Code key="p">{project}</Code>]] as Row[]) : []),
    [t("upstream.credential.region"), credential.region ? <Code key="r">{credential.region}</Code> : "—"],
  ];
}
