import type { AdminSchemas } from "@axiom/api";
import { ActionButton, Cluster, EmptyState, Table, Tag, Td, Text, useFormat } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { Code, Head, State, Time } from "../parts";
import { endpointState, testFormats } from "./endpoint-test";
import type { Open } from "./open";

type OnOpen = ((open: Open) => void) | undefined;

function Empty({ what }: { what: "groups" | "credentials" | "unbound" | "endpoints" | "endpoints.unavailable" }) {
  const { t } = useTranslation();
  return (
    <EmptyState compact title={t(`suppliers.${what}.empty.title`)}>
      {t(`suppliers.${what}.empty.body`)}
    </EmptyState>
  );
}

// The name a group goes by in a list of its keys or endpoints: the code name once it has one.
function groupName(groups: AdminSchemas["SupplierGroup"][], id: string) {
  const group = groups.find((candidate) => candidate.id === id);
  return group?.codename ?? group?.name ?? id;
}

// The groups the supplier lists, with the ratio it charges for each and how the key Axiom uses for it stands. The sync
// puts a group with a priced model in use and makes its key; a person can stop using one or turn it back on.
export function Groups({ groups, onOpen }: { groups: AdminSchemas["SupplierGroup"][]; onOpen: OnOpen }) {
  const { t } = useTranslation();
  if (groups.length === 0) {
    return <Empty what="groups" />;
  }
  return (
    <Table label={t("suppliers.groups.title")}>
      <Head
        cols={[
          [t("suppliers.groups.name"), { main: true }],
          t("suppliers.groups.ratio"),
          t("suppliers.groups.codename"),
          t("suppliers.col.status"),
        ]}
        action={onOpen !== undefined}
      />
      <tbody>
        {groups.map((group) => (
          <GroupRow key={group.id} group={group} onOpen={onOpen} />
        ))}
      </tbody>
    </Table>
  );
}

function GroupRow({ group, onOpen }: { group: AdminSchemas["SupplierGroup"]; onOpen: OnOpen }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <tr>
      <Td main>
        <Text>{group.name}</Text>
        <Text size="sm" tone="faint">
          <Code>{group.upstream_ref}</Code>
        </Text>
        {group.key_state === "failed" && group.key_reason ? (
          <Text size="sm" tone="bad">
            {t(`suppliers.groups.keyReason.${group.key_reason}`)}
          </Text>
        ) : null}
      </Td>
      <Td align="end">{`×${format.decimal(group.ratio)}`}</Td>
      <Td>{group.codename ? <Code>{group.codename}</Code> : "—"}</Td>
      <Td>
        <Cluster gap={4}>
          <State tone={group.selected ? "good" : "neutral"}>
            {group.selected ? t("suppliers.groups.selected") : t("suppliers.groups.notSelected")}
          </State>
          {group.present ? null : <State tone="warn">{t("suppliers.groups.gone")}</State>}
          {group.key_state ? (
            <State tone={keyTones[group.key_state]}>{t(`suppliers.groups.key.${group.key_state}`)}</State>
          ) : null}
        </Cluster>
      </Td>
      {onOpen ? (
        <Td actions>
          <ActionButton size="sm" onClick={() => onOpen({ kind: "group", group })}>
            {group.selected ? t("suppliers.group.unselect.action") : t("suppliers.group.select.action")}
          </ActionButton>
        </Td>
      ) : null}
    </tr>
  );
}

const keyTones = {
  pending: "neutral",
  ready: "good",
  failed: "bad",
  unavailable: "neutral",
} as const satisfies Record<
  NonNullable<AdminSchemas["SupplierGroup"]["key_state"]>,
  "neutral" | "good" | "warn" | "bad"
>;

const credentialTones = {
  binding: "neutral",
  active: "good",
  upstream_disabled: "warn",
  quota_exhausted: "warn",
  expired: "bad",
  disabled: "warn",
  erased: "neutral",
} as const satisfies Record<AdminSchemas["SupplierCredential"]["status"], "neutral" | "good" | "warn" | "bad">;

// The keys Axiom uses for groups, made by the sync or bound by a person: what each group's calls are sent with.
export function Credentials({
  credentials,
  groups,
}: {
  credentials: AdminSchemas["SupplierCredential"][];
  groups: AdminSchemas["SupplierGroup"][];
}) {
  const { t } = useTranslation();
  const format = useFormat();
  if (credentials.length === 0) {
    return <Empty what="credentials" />;
  }
  return (
    <Table label={t("suppliers.credentials.title")}>
      <Head
        cols={[
          [t("suppliers.credentials.key"), { main: true }],
          t("suppliers.credentials.group"),
          t("suppliers.col.status"),
          t("suppliers.credentials.until"),
          [t("suppliers.credentials.concurrency"), { align: "end" }],
        ]}
      />
      <tbody>
        {credentials.map((credential) => {
          return (
            <tr key={credential.id}>
              <Td main>
                <Code>{credential.upstream_key_ref}</Code>
              </Td>
              <Td>{groupName(groups, credential.group_id)}</Td>
              <Td>
                <State tone={credentialTones[credential.status]}>
                  {t(`suppliers.credentials.state.${credential.status}`)}
                </State>
              </Td>
              <Td>
                {credential.cooldown_until ? (
                  <>
                    {t("suppliers.credentials.cooldown")} <Time at={credential.cooldown_until} />
                  </>
                ) : (
                  <Time at={credential.expires_at} />
                )}
              </Td>
              <Td align="end">{format.number(credential.max_concurrency)}</Td>
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
          [t("suppliers.credentials.key"), { main: true }],
          t("suppliers.unbound.groupRef"),
          t("suppliers.col.status"),
        ]}
        action={onOpen !== undefined}
      />
      <tbody>
        {keys.map((key) => (
          <tr key={key.ref}>
            <Td main>
              <Code>{key.ref}</Code>
            </Td>
            <Td>{key.group_ref ? <Code>{key.group_ref}</Code> : "—"}</Td>
            <Td>{t(`suppliers.unbound.state.${key.status}`)}</Td>
            {onOpen ? (
              <Td actions>
                <ActionButton size="sm" onClick={() => onOpen({ kind: "bind", key })}>
                  {t("suppliers.bind.action")}
                </ActionButton>
              </Td>
            ) : null}
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

const endpointTones = {
  off: "neutral",
  unknown: "warn",
  conflict: "bad",
  ready: "good",
} as const satisfies Record<ReturnType<typeof endpointState>, "neutral" | "good" | "warn" | "bad">;

// The supplier's endpoints, one per model of a group, by upstream name: what a person tests. Only one that may be called
// in a format a test can use offers the test; the state says why another is not called. Two groups can offer the same model at different costs, so the
// group is always shown with the name and is part of the button's name. None from
// the server is a node of an earlier release (SupplierView).
export function Endpoints({
  endpoints,
  groups,
  onOpen,
}: {
  endpoints: AdminSchemas["SupplierEndpoint"][] | undefined;
  groups: AdminSchemas["SupplierGroup"][];
  onOpen: OnOpen;
}) {
  const { t } = useTranslation();
  if (endpoints === undefined) {
    return <Empty what="endpoints.unavailable" />;
  }
  if (endpoints.length === 0) {
    return <Empty what="endpoints" />;
  }
  return (
    <Table label={t("suppliers.endpoints.title")}>
      <Head
        cols={[
          [t("suppliers.endpoints.model"), { main: true }],
          t("suppliers.endpoints.group"),
          t("suppliers.endpoints.formats"),
          t("suppliers.endpoints.quantization"),
          t("suppliers.col.status"),
        ]}
        action={onOpen !== undefined}
      />
      <tbody>
        {endpoints.map((endpoint) => (
          <EndpointRow
            key={endpoint.id}
            endpoint={endpoint}
            group={groupName(groups, endpoint.group_id)}
            onOpen={onOpen}
          />
        ))}
      </tbody>
    </Table>
  );
}

function EndpointRow({
  endpoint,
  group,
  onOpen,
}: {
  endpoint: AdminSchemas["SupplierEndpoint"];
  group: string;
  onOpen: OnOpen;
}) {
  const { t } = useTranslation();
  const state = endpointState(endpoint);
  return (
    <tr>
      <Td main>
        <Code>{endpoint.upstream_name}</Code>
      </Td>
      <Td>{group}</Td>
      <Td>
        <Cluster gap={4}>
          {endpoint.formats.map((format) => (
            <Tag key={format}>{t(`suppliers.test.format_${format}`)}</Tag>
          ))}
        </Cluster>
      </Td>
      <Td>{endpoint.quantization ?? t("suppliers.quantization.none")}</Td>
      <Td>
        <State tone={endpointTones[state]}>{t(`suppliers.endpoints.state.${state}`)}</State>
      </Td>
      {onOpen ? (
        <Td actions>
          <Cluster gap={4} justify="end">
            {state === "ready" && testFormats(endpoint.formats).length > 0 ? (
              <ActionButton
                size="sm"
                aria-label={t("suppliers.test.title", { name: endpoint.upstream_name, group })}
                onClick={() => onOpen({ kind: "test", endpoint, group })}
              >
                {t("suppliers.test.open")}
              </ActionButton>
            ) : null}
            <ActionButton
              size="sm"
              aria-label={t("suppliers.quantization.title", { name: endpoint.upstream_name })}
              onClick={() => onOpen({ kind: "quantization", endpoint })}
            >
              {t("suppliers.quantization.open")}
            </ActionButton>
          </Cluster>
        </Td>
      ) : null}
    </tr>
  );
}
