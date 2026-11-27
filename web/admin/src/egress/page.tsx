import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import {
  ActionButton,
  Card,
  Cluster,
  DataState,
  EmptyState,
  ForbiddenState,
  PageHead,
  Table,
  Td,
  Text,
} from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import en from "../locales/en/egress";
import zh from "../locales/zh/egress";
import { Code, Head, PageBody, State } from "../parts";
import { egressProxiesQuery } from "../suppliers/egress";
import { CreateProxy, EditProxy, RemoveProxy } from "./dialogs";
import { credentialExitsQuery } from "./queries";
import { usage } from "./read";

export const messages = { zh: { egress: zh }, en: { egress: en } };

type Proxy = AdminSchemas["EgressProxy"];
type Exits = { data?: string[]; isError: boolean };
type Editing = { kind: "create" } | { kind: "edit" | "remove"; id: string };

// EgressProxies is where the platform's egress proxies are added, changed and removed (suppliers.md「出口代理」); a
// key's exit is picked from them on the key's own page.
export default function EgressProxies() {
  const { t } = useTranslation();
  const session = useSession();
  return (
    <PageBody>
      <PageHead title={t("egress.title")} lede={t("egress.lede")} />
      {can(session, "suppliers.view") ? <Proxies manage={can(session, "suppliers.manage")} /> : <ForbiddenState />}
    </PageBody>
  );
}

function Proxies({ manage }: { manage: boolean }) {
  const { t } = useTranslation();
  const list = useQuery(egressProxiesQuery);
  const exits = useQuery(credentialExitsQuery);
  const [editing, setEditing] = useState<Editing>();
  const proxies = list.data?.data ?? [];
  // Follow the proxy in the fetched list, so a dialog opened on one that was removed meanwhile is not left open.
  const target = editing && editing.kind !== "create" ? proxies.find((p) => p.id === editing.id) : undefined;
  const add = manage ? (
    <ActionButton variant="pri" icon="plus" onClick={() => setEditing({ kind: "create" })}>
      {t("egress.create")}
    </ActionButton>
  ) : undefined;
  return (
    <>
      <Card layout="flush" title={t("egress.listTitle")} actions={proxies.length > 0 ? add : undefined}>
        <DataState
          query={list}
          isEmpty={(view) => view.data.length === 0}
          empty={
            <EmptyState title={t("egress.empty.title")} action={add}>
              {t("egress.empty.body")}
            </EmptyState>
          }
        >
          {(view) => (
            <Table label={t("egress.listTitle")}>
              <Head
                cols={[
                  [t("egress.name"), { main: true }],
                  t("egress.scheme"),
                  [t("egress.address"), { from: "sm" }],
                  t("egress.inUse"),
                  t("egress.status"),
                ]}
                action={manage}
              />
              <tbody>
                {view.data.map((proxy) => (
                  <Row
                    key={proxy.id}
                    proxy={proxy}
                    proxies={view.data}
                    exits={exits}
                    manage={manage}
                    onEdit={() => setEditing({ kind: "edit", id: proxy.id })}
                    onRemove={() => setEditing({ kind: "remove", id: proxy.id })}
                  />
                ))}
              </tbody>
            </Table>
          )}
        </DataState>
      </Card>
      {editing?.kind === "create" ? <CreateProxy proxies={proxies} onClose={() => setEditing(undefined)} /> : null}
      {target && editing?.kind === "edit" ? (
        <EditProxy key={target.id} proxy={target} proxies={proxies} onClose={() => setEditing(undefined)} />
      ) : null}
      {target && editing?.kind === "remove" ? (
        <RemoveProxy
          key={target.id}
          proxy={target}
          use={usage(target, proxies, exits.data ?? [])}
          onClose={() => setEditing(undefined)}
        />
      ) : null}
    </>
  );
}

function Row({
  proxy,
  proxies,
  exits,
  manage,
  onEdit,
  onRemove,
}: {
  proxy: Proxy;
  proxies: readonly Proxy[];
  exits: Exits;
  manage: boolean;
  onEdit: () => void;
  onRemove: () => void;
}) {
  const { t } = useTranslation();
  return (
    <tr>
      <Td main>
        <Text medium>{proxy.name}</Text> <Code>{proxy.id}</Code>
        {proxy.fallback_id ? (
          <Text size="sm" tone="muted">
            {t("egress.fallbackIs", { id: proxy.fallback_id })}
          </Text>
        ) : null}
      </Td>
      <Td>{proxy.scheme}</Td>
      <Td from="sm">
        <Code>{`${proxy.host.includes(":") ? `[${proxy.host}]` : proxy.host}:${proxy.port}`}</Code>
      </Td>
      <Td>
        <InUse proxy={proxy} proxies={proxies} exits={exits} />
      </Td>
      <Td>
        <State tone={proxy.status === "active" ? "good" : "neutral"}>{t(`egress.statuses.${proxy.status}`)}</State>
      </Td>
      {manage ? (
        <Td actions>
          <Cluster>
            <ActionButton size="sm" onClick={onEdit}>
              {t("egress.edit")}
            </ActionButton>
            <ActionButton size="sm" onClick={onRemove}>
              {t("egress.remove")}
            </ActionButton>
          </Cluster>
        </Td>
      ) : null}
    </tr>
  );
}

// What the credentials' exits say is read apart from the proxies and may still be loading or have failed; the cell then
// says so rather than claiming a proxy is free.
function InUse({ proxy, proxies, exits }: { proxy: Proxy; proxies: readonly Proxy[]; exits: Exits }) {
  const { t } = useTranslation();
  if (!exits.data) return <Text tone="muted">{exits.isError ? t("egress.useUnknown") : "…"}</Text>;
  const use = usage(proxy, proxies, exits.data);
  if (use.credentials + use.fallbackOf === 0) return <Text tone="muted">{t("egress.unused")}</Text>;
  return <Text>{t("egress.used", use)}</Text>;
}
