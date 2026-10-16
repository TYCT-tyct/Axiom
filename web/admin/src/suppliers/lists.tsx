import type { AdminSchemas } from "@axiom/api";
import { Button, EmptyState, Table, Td } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { Code, Head, State, Time } from "../parts";
import type { Open } from "./open";

type OnOpen = ((open: Open) => void) | undefined;

function Empty({ what }: { what: "groups" | "credentials" | "unbound" }) {
  const { t } = useTranslation();
  return (
    <EmptyState compact title={t(`suppliers.${what}.empty.title`)}>
      {t(`suppliers.${what}.empty.body`)}
    </EmptyState>
  );
}

// The groups the supplier lists, with the ratio it charges for each. Choosing one gives it its code name and makes its
// models endpoints.
export function Groups({ groups, onOpen }: { groups: AdminSchemas["SupplierGroup"][]; onOpen: OnOpen }) {
  const { t } = useTranslation();
  if (groups.length === 0) {
    return <Empty what="groups" />;
  }
  return (
    <Table label={t("suppliers.groups.title")}>
      <Head
        cols={[
          [t("suppliers.groups.name")],
          [t("suppliers.groups.ratio")],
          [t("suppliers.groups.codename"), "hidden md:table-cell"],
          [t("suppliers.col.status")],
        ]}
        action={onOpen !== undefined}
      />
      <tbody>
        {groups.map((group) => (
          <tr key={group.id}>
            <Td className="max-w-56 min-w-32">
              <p className="truncate text-t1" title={group.name}>
                {group.name}
              </p>
              <p className="truncate text-label text-t4" title={group.upstream_ref}>
                <Code>{group.upstream_ref}</Code>
              </p>
            </Td>
            <Td className="tabular-nums">{`×${group.ratio}`}</Td>
            <Td className="hidden max-w-48 truncate md:table-cell">
              {group.codename ? <Code>{group.codename}</Code> : "—"}
            </Td>
            <Td>
              <div className="flex flex-wrap gap-1">
                <State tone={group.selected ? "good" : "neutral"}>
                  {group.selected ? t("suppliers.groups.selected") : t("suppliers.groups.notSelected")}
                </State>
                {group.present ? null : <State tone="warn">{t("suppliers.groups.gone")}</State>}
              </div>
            </Td>
            {onOpen ? (
              <Td className="whitespace-nowrap">
                <Button size="sm" onClick={() => onOpen({ kind: "group", group })}>
                  {group.selected ? t("suppliers.group.unselect.action") : t("suppliers.group.select.action")}
                </Button>
              </Td>
            ) : null}
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

const credentialTones = {
  binding: "neutral",
  active: "good",
  upstream_disabled: "warn",
  quota_exhausted: "warn",
  expired: "bad",
  disabled: "warn",
} as const satisfies Record<AdminSchemas["SupplierCredential"]["status"], "neutral" | "good" | "warn" | "bad">;

// The keys bound to groups: what Axiom sends each group's calls with.
export function Credentials({
  credentials,
  groups,
}: {
  credentials: AdminSchemas["SupplierCredential"][];
  groups: AdminSchemas["SupplierGroup"][];
}) {
  const { t } = useTranslation();
  if (credentials.length === 0) {
    return <Empty what="credentials" />;
  }
  return (
    <Table label={t("suppliers.credentials.title")}>
      <Head
        cols={[
          [t("suppliers.credentials.key")],
          [t("suppliers.credentials.group")],
          [t("suppliers.col.status")],
          [t("suppliers.credentials.until"), "hidden lg:table-cell"],
          [t("suppliers.credentials.concurrency"), "hidden md:table-cell"],
        ]}
      />
      <tbody>
        {credentials.map((credential) => {
          const group = groups.find((candidate) => candidate.id === credential.group_id);
          return (
            <tr key={credential.id}>
              <Td className="max-w-48 truncate">
                <Code>{credential.upstream_key_ref}</Code>
              </Td>
              <Td className="max-w-48 truncate">{group?.codename ?? group?.name ?? credential.group_id}</Td>
              <Td>
                <State tone={credentialTones[credential.status]}>
                  {t(`suppliers.credentials.state.${credential.status}`)}
                </State>
              </Td>
              <Td className="hidden whitespace-nowrap lg:table-cell">
                {credential.cooldown_until ? (
                  <>
                    {t("suppliers.credentials.cooldown")} <Time at={credential.cooldown_until} />
                  </>
                ) : (
                  <Time at={credential.expires_at} />
                )}
              </Td>
              <Td className="hidden tabular-nums md:table-cell">{credential.max_concurrency}</Td>
            </tr>
          );
        })}
      </tbody>
    </Table>
  );
}

// The keys the supplier lists that Axiom has not bound to a group, which a person can bind.
export function UnboundKeys({ keys, onOpen }: { keys: AdminSchemas["UpstreamKey"][]; onOpen: OnOpen }) {
  const { t } = useTranslation();
  if (keys.length === 0) {
    return <Empty what="unbound" />;
  }
  return (
    <Table label={t("suppliers.unbound.title")}>
      <Head
        cols={[
          [t("suppliers.credentials.key")],
          [t("suppliers.unbound.groupRef"), "hidden md:table-cell"],
          [t("suppliers.col.status")],
        ]}
        action={onOpen !== undefined}
      />
      <tbody>
        {keys.map((key) => (
          <tr key={key.ref}>
            <Td className="max-w-56 truncate">
              <Code>{key.ref}</Code>
            </Td>
            <Td className="hidden max-w-48 truncate md:table-cell">
              {key.group_ref ? <Code>{key.group_ref}</Code> : "—"}
            </Td>
            <Td>{t(`suppliers.unbound.state.${key.status}`)}</Td>
            {onOpen ? (
              <Td className="whitespace-nowrap">
                <Button size="sm" onClick={() => onOpen({ kind: "bind", key })}>
                  {t("suppliers.bind.action")}
                </Button>
              </Td>
            ) : null}
          </tr>
        ))}
      </tbody>
    </Table>
  );
}
