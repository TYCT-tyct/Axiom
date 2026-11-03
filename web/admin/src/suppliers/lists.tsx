import type { AdminSchemas } from "@axiom/api";
import { Button, EmptyState, Table, Td, useFormat } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { Code, Head, mainCell, State, Time } from "../parts";
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
          [t("suppliers.groups.name")],
          [t("suppliers.groups.ratio")],
          [t("suppliers.groups.codename"), "hidden md:table-cell"],
          [t("suppliers.col.status")],
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
      <Td className={mainCell}>
        <p className="truncate text-t1" title={group.name}>
          {group.name}
        </p>
        <p className="truncate text-label text-t4" title={group.upstream_ref}>
          <Code>{group.upstream_ref}</Code>
        </p>
        {group.key_state === "failed" && group.key_reason ? (
          <p className="text-label text-bad">{t(`suppliers.groups.keyReason.${group.key_reason}`)}</p>
        ) : null}
      </Td>
      <Td className="tabular-nums">{`×${format.decimal(group.ratio)}`}</Td>
      <Td className="hidden max-w-48 truncate md:table-cell">{group.codename ? <Code>{group.codename}</Code> : "—"}</Td>
      <Td>
        <div className="flex flex-wrap gap-1">
          <State tone={group.selected ? "good" : "neutral"}>
            {group.selected ? t("suppliers.groups.selected") : t("suppliers.groups.notSelected")}
          </State>
          {group.present ? null : <State tone="warn">{t("suppliers.groups.gone")}</State>}
          {group.key_state ? (
            <State tone={keyTones[group.key_state]}>{t(`suppliers.groups.key.${group.key_state}`)}</State>
          ) : null}
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
          [t("suppliers.credentials.key")],
          [t("suppliers.credentials.group")],
          [t("suppliers.col.status")],
          [t("suppliers.credentials.until"), "hidden lg:table-cell"],
          [t("suppliers.credentials.concurrency"), "hidden md:table-cell"],
        ]}
      />
      <tbody>
        {credentials.map((credential) => {
          return (
            <tr key={credential.id}>
              <Td className="max-w-48">
                <p className="truncate" title={credential.upstream_key_ref}>
                  <Code>{credential.upstream_key_ref}</Code>
                </p>
              </Td>
              <Td className="max-w-48 truncate">{groupName(groups, credential.group_id)}</Td>
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
              <Td className="hidden tabular-nums md:table-cell">{format.number(credential.max_concurrency)}</Td>
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
          [t("suppliers.col.status"), "whitespace-nowrap"],
        ]}
        action={onOpen !== undefined}
      />
      <tbody>
        {keys.map((key) => (
          <tr key={key.ref}>
            <Td className={mainCell}>
              <p className="truncate" title={key.ref}>
                <Code>{key.ref}</Code>
              </p>
            </Td>
            <Td className="hidden max-w-48 truncate md:table-cell">
              {key.group_ref ? <Code>{key.group_ref}</Code> : "—"}
            </Td>
            <Td className="whitespace-nowrap">{t(`suppliers.unbound.state.${key.status}`)}</Td>
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

const endpointTones = {
  off: "neutral",
  unknown: "warn",
  conflict: "bad",
  ready: "good",
} as const satisfies Record<ReturnType<typeof endpointState>, "neutral" | "good" | "warn" | "bad">;

// The supplier's endpoints, one per model of a group, by upstream name: what a person tests. Only one that may be called
// in a format a test can use offers the test; the state says why another is not called. Two groups can offer the same model at different costs, so the
// group is always shown with the name (under it where its column is hidden) and is part of the button's name. None from
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
          [t("suppliers.endpoints.model")],
          [t("suppliers.endpoints.group"), "hidden md:table-cell"],
          [t("suppliers.endpoints.formats"), "hidden lg:table-cell"],
          [t("suppliers.endpoints.quantization"), "hidden xl:table-cell"],
          [t("suppliers.col.status"), "whitespace-nowrap"],
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
      <Td className={mainCell}>
        <p className="truncate" title={endpoint.upstream_name}>
          <Code>{endpoint.upstream_name}</Code>
        </p>
        <p className="truncate text-label text-t3 md:hidden" title={group}>
          {group}
        </p>
      </Td>
      <Td className="hidden max-w-48 truncate md:table-cell">{group}</Td>
      <Td className="hidden lg:table-cell">
        <div className="flex flex-wrap gap-1">
          {endpoint.formats.map((format) => (
            <span key={format} className="rounded-xs bg-s3 px-1.5 py-0.5 text-label text-t2">
              {t(`suppliers.test.format_${format}`)}
            </span>
          ))}
        </div>
      </Td>
      <Td className="hidden whitespace-nowrap xl:table-cell">
        {endpoint.quantization ?? t("suppliers.quantization.none")}
      </Td>
      <Td className="whitespace-nowrap">
        <State tone={endpointTones[state]}>{t(`suppliers.endpoints.state.${state}`)}</State>
      </Td>
      {onOpen ? (
        <Td className="whitespace-nowrap">
          <div className="flex justify-end gap-1">
            {state === "ready" && testFormats(endpoint.formats).length > 0 ? (
              <Button
                size="sm"
                aria-label={t("suppliers.test.title", { name: endpoint.upstream_name, group })}
                onClick={() => onOpen({ kind: "test", endpoint, group })}
              >
                {t("suppliers.test.open")}
              </Button>
            ) : null}
            {/* A phone has room beside the status for one button, and the precision column itself is shown only from
                xl up; the precision is set from a wider window. */}
            <Button
              size="sm"
              variant="secondary"
              className="hidden sm:inline-flex"
              aria-label={t("suppliers.quantization.title", { name: endpoint.upstream_name })}
              onClick={() => onOpen({ kind: "quantization", endpoint })}
            >
              {t("suppliers.quantization.open")}
            </Button>
          </div>
        </Td>
      ) : null}
    </tr>
  );
}
