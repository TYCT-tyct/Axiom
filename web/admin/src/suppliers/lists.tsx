import type { AdminSchemas } from "@axiom/api";
import { ActionButton, Cluster, EmptyState, Table, Tag, Td } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { Code, Head, State } from "../parts";
import { endpointState, testFormats } from "./endpoint-test";
import type { Open } from "./open";

type OnOpen = ((open: Open) => void) | undefined;

function Empty({ what }: { what: "unbound" | "endpoints" | "endpoints.unavailable" }) {
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
  onShow,
}: {
  endpoints: AdminSchemas["SupplierEndpoint"][] | undefined;
  groups: AdminSchemas["SupplierGroup"][];
  onOpen: OnOpen;
  onShow: (endpoint: AdminSchemas["SupplierEndpoint"]) => void;
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
        action
      />
      <tbody>
        {endpoints.map((endpoint) => (
          <EndpointRow
            key={endpoint.id}
            endpoint={endpoint}
            group={groupName(groups, endpoint.group_id)}
            onOpen={onOpen}
            onShow={() => onShow(endpoint)}
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
  onShow,
}: {
  endpoint: AdminSchemas["SupplierEndpoint"];
  group: string;
  onOpen: OnOpen;
  onShow: () => void;
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
      <Td actions>
        <Cluster gap={4} justify="end">
          <ActionButton
            size="sm"
            aria-label={t("upstream.endpoint.open", { name: endpoint.upstream_name })}
            onClick={onShow}
          >
            {t("upstream.byModel.details")}
          </ActionButton>
          {onOpen ? <EndpointChanges endpoint={endpoint} group={group} state={state} onOpen={onOpen} /> : null}
        </Cluster>
      </Td>
    </tr>
  );
}

function EndpointChanges({
  endpoint,
  group,
  state,
  onOpen,
}: {
  endpoint: AdminSchemas["SupplierEndpoint"];
  group: string;
  state: ReturnType<typeof endpointState>;
  onOpen: (open: Open) => void;
}) {
  const { t } = useTranslation();
  return (
    <>
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
        aria-label={t(
          endpoint.enabled ? "suppliers.endpointEnabled.disable.title" : "suppliers.endpointEnabled.enable.title",
          { name: endpoint.upstream_name, group },
        )}
        onClick={() => onOpen({ kind: "endpointEnabled", endpoint, group })}
      >
        {t(endpoint.enabled ? "suppliers.endpointEnabled.disable.action" : "suppliers.endpointEnabled.enable.action")}
      </ActionButton>
      <ActionButton
        size="sm"
        aria-label={t("suppliers.quantization.title", { name: endpoint.upstream_name })}
        onClick={() => onOpen({ kind: "quantization", endpoint })}
      >
        {t("suppliers.quantization.open")}
      </ActionButton>
    </>
  );
}
