import type { Schemas } from "@axiom/api";
import { consoleApi, unwrap, unwrapEmpty } from "@axiom/api";
import { accountKey, can, useErrors, useSession } from "@axiom/app";
import {
  Badge,
  Button,
  DataState,
  Dialog,
  EmptyState,
  Icon,
  PageHeader,
  Panel,
  Table,
  Td,
  Th,
  useFormat,
} from "@axiom/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { AddProviderKeyDialog } from "../provider-key-dialog";
import { providerKeysQuery } from "../queries";

type ProviderKey = Schemas["ProviderKey"];

// ProviderKeys lists the account's own provider keys and lets it add, test, switch and remove them. A key is never
// shown again: the row keeps the last four characters only.
export default function ProviderKeys() {
  const { t } = useTranslation();
  const session = useSession();
  const view = can(session, "api_keys.view");
  const manage = can(session, "api_keys.manage");
  const keys = useQuery(providerKeysQuery);
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<ProviderKey | null>(null);
  const [tested, setTested] = useState<Record<string, string>>({});
  const rows = keys.data?.data ?? [];
  return (
    <>
      <PageHeader title={t("nav.providerKeys")} description={t("providerKeys.description")} />
      <div className="space-y-4 p-4 sm:p-6">
        {manage ? (
          <Button variant="primary" size="md" onClick={() => setAdding(true)}>
            <Icon name="plus" />
            {t("providerKeys.add")}
          </Button>
        ) : null}
        {!view ? (
          <Panel>
            <EmptyState title={t("providerKeys.noPermission.title")}>{t("providerKeys.noPermission.body")}</EmptyState>
          </Panel>
        ) : (
          <Panel>
            <DataState
              query={keys}
              isEmpty={() => rows.length === 0}
              empty={<EmptyState title={t("providerKeys.empty.title")}>{t("providerKeys.empty.body")}</EmptyState>}
            >
              {() => (
                <KeyTable
                  rows={rows}
                  manage={manage}
                  tested={tested}
                  onTested={(id, text) => setTested((all) => ({ ...all, [id]: text }))}
                  onRemove={setRemoving}
                />
              )}
            </DataState>
          </Panel>
        )}
      </div>
      {adding ? <AddProviderKeyDialog onClose={() => setAdding(false)} /> : null}
      {removing ? <RemoveProviderKeyDialog providerKey={removing} onClose={() => setRemoving(null)} /> : null}
    </>
  );
}

// KeyTable is the list itself: a row per key, with no state of its own, so the page stays about which states it shows.
function KeyTable({
  rows,
  manage,
  tested,
  onTested,
  onRemove,
}: {
  rows: ProviderKey[];
  manage: boolean;
  tested: Record<string, string>;
  onTested: (id: string, text: string) => void;
  onRemove: (providerKey: ProviderKey) => void;
}) {
  const { t } = useTranslation();
  return (
    <Table label={t("nav.providerKeys")}>
      <thead>
        <tr>
          <Th>{t("providerKeys.name")}</Th>
          <Th>{t("providerKeys.provider")}</Th>
          <Th>{t("providerKeys.status")}</Th>
          <Th>{t("providerKeys.added")}</Th>
          <Th />
        </tr>
      </thead>
      <tbody>
        {rows.map((key) => (
          <Row
            key={key.id}
            providerKey={key}
            manage={manage}
            tested={tested[key.id]}
            onTested={(text) => onTested(key.id, text)}
            onRemove={() => onRemove(key)}
          />
        ))}
      </tbody>
    </Table>
  );
}

function Row({
  providerKey,
  manage,
  tested,
  onTested,
  onRemove,
}: {
  providerKey: ProviderKey;
  manage: boolean;
  tested: string | undefined;
  onTested: (text: string) => void;
  onRemove: () => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <tr>
      <Td>
        <div className="font-medium">{providerKey.name}</div>
        <div className="font-mono text-xs text-t3">{`•••• ${providerKey.hint ?? ""}`}</div>
      </Td>
      <Td className="capitalize">{providerKey.provider}</Td>
      <Td>
        <Badge tone={providerKey.rejected ? "bad" : providerKey.enabled ? "ok" : "neutral"}>
          {providerKey.rejected
            ? t("providerKeys.rejected")
            : providerKey.enabled
              ? t("keys.enabled")
              : t("keys.disabled")}
        </Badge>
        {!providerKey.fallback ? <div className="mt-1 text-xs text-t3">{t("providerKeys.noFallback")}</div> : null}
        {tested ? <div className="mt-1 text-xs text-t3">{tested}</div> : null}
      </Td>
      <Td className="text-t3">{format.dateTime(new Date(providerKey.created_at))}</Td>
      <Td>{manage ? <Actions providerKey={providerKey} onTested={onTested} onRemove={onRemove} /> : null}</Td>
    </tr>
  );
}

function RemoveProviderKeyDialog({ providerKey, onClose }: { providerKey: ProviderKey; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: () =>
      unwrapEmpty(
        consoleApi.DELETE("/api/console/v1/provider-keys/{provider_key_id}", {
          params: { path: { provider_key_id: providerKey.id } },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: [accountKey, "provider-keys"] });
      onClose();
    },
  });
  return (
    <Dialog title={t("providerKeys.remove")} onClose={onClose}>
      <div className="space-y-4">
        <p className="text-t2 [overflow-wrap:anywhere]">{t("providerKeys.removeBody", { name: providerKey.name })}</p>
        {remove.isError ? <span className="text-danger">{errors.message(remove.error)}</span> : null}
        <div className="flex justify-end gap-2">
          <Button size="md" onClick={onClose}>
            {t("cancel", { ns: "common" })}
          </Button>
          <Button size="md" variant="danger" onClick={() => remove.mutate()} disabled={remove.isPending}>
            {t("providerKeys.remove")}
          </Button>
        </div>
      </div>
    </Dialog>
  );
}

// Actions are the three things an account may do to a key. They live in their own component so the row's rendering
// stays simple.
function Actions({
  providerKey,
  onTested,
  onRemove,
}: {
  providerKey: ProviderKey;
  onTested: (text: string) => void;
  onRemove: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const queryClient = useQueryClient();
  const refresh = () => queryClient.invalidateQueries({ queryKey: [accountKey, "provider-keys"] });
  const toggle = useMutation({
    mutationFn: () =>
      unwrap(
        consoleApi.PUT("/api/console/v1/provider-keys/{provider_key_id}/enabled", {
          params: { path: { provider_key_id: providerKey.id } },
          body: { enabled: !providerKey.enabled },
        }),
      ),
    onSuccess: refresh,
  });
  const test = useMutation({
    mutationFn: () =>
      unwrap(
        consoleApi.POST("/api/console/v1/provider-keys/{provider_key_id}/test", {
          params: { path: { provider_key_id: providerKey.id } },
        }),
      ),
    onSuccess: async (result) => {
      onTested(
        result.inconclusive
          ? t("providerKeys.test.inconclusive")
          : result.ok
            ? t("providerKeys.test.ok")
            : t("providerKeys.test.refused"),
      );
      await refresh();
    },
  });
  return (
    <div className="flex flex-wrap justify-end gap-2">
      <Button size="sm" onClick={() => test.mutate()} disabled={test.isPending}>
        {test.isPending ? t("providerKeys.testing") : t("providerKeys.test.action")}
      </Button>
      <Button size="sm" onClick={() => toggle.mutate()} disabled={toggle.isPending || providerKey.rejected}>
        {providerKey.enabled ? t("providerKeys.disable") : t("providerKeys.enable")}
      </Button>
      <Button size="sm" variant="danger" onClick={onRemove}>
        {t("providerKeys.remove")}
      </Button>
      {test.isError || toggle.isError ? (
        <span className="text-xs text-danger">{errors.message(test.error ?? toggle.error)}</span>
      ) : null}
    </div>
  );
}
