import type { Schemas } from "@axiom/api";
import { can, LoadMore, useSession } from "@axiom/app";
import { Badge, Button, DataState, EmptyState, Icon, PageHeader, Panel, Table, Td, Th, useFormat } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { CreateKeyDialog, DeleteKeyDialog, EditKeyDialog, SecretDialog } from "../key-dialogs";
import { maskedKey } from "../parts";
import { keysQuery } from "../queries";

type Key = Schemas["Key"];
type Dialog =
  | { kind: "create" }
  | { kind: "edit"; key: Key }
  | { kind: "delete"; key: Key }
  | { kind: "secret"; created: Schemas["CreatedKey"] };

export default function Keys() {
  const { t } = useTranslation();
  const manage = can(useSession(), "api_keys.manage");
  const keys = useInfiniteQuery(keysQuery);
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const close = () => setDialog(null);
  return (
    <>
      <PageHeader title={t("nav.keys")} description={t("keys.description")} />
      <div className="space-y-4 p-4 sm:p-6">
        {manage ? (
          <Button variant="primary" size="md" onClick={() => setDialog({ kind: "create" })}>
            <Icon name="plus" />
            {t("keys.create")}
          </Button>
        ) : null}
        <Panel>
          <DataState
            query={keys}
            isEmpty={(data) => data.pages[0]?.data.length === 0}
            empty={<EmptyState title={t("keys.empty.title")}>{t("keys.empty.body")}</EmptyState>}
          >
            {(data) => (
              <KeysTable
                keys={data.pages.flatMap((page) => page.data)}
                manage={manage}
                onEdit={(key) => setDialog({ kind: "edit", key })}
                onDelete={(key) => setDialog({ kind: "delete", key })}
              />
            )}
          </DataState>
          <LoadMore list={keys} />
        </Panel>
      </div>
      {dialog?.kind === "create" ? (
        <CreateKeyDialog onClose={close} onCreated={(created) => setDialog({ kind: "secret", created })} />
      ) : null}
      {dialog?.kind === "edit" ? <EditKeyDialog apiKey={dialog.key} onClose={close} /> : null}
      {dialog?.kind === "delete" ? <DeleteKeyDialog apiKey={dialog.key} onClose={close} /> : null}
      {dialog?.kind === "secret" ? <SecretDialog created={dialog.created} onClose={close} /> : null}
    </>
  );
}

function KeysTable({
  keys,
  manage,
  onEdit,
  onDelete,
}: {
  keys: Key[];
  manage: boolean;
  onEdit: (key: Key) => void;
  onDelete: (key: Key) => void;
}) {
  const { t } = useTranslation();
  return (
    <Table label={t("nav.keys")}>
      <thead>
        <tr>
          <Th>{t("keys.col.name")}</Th>
          <Th>{t("keys.col.status")}</Th>
          <Th className="hidden md:table-cell">{t("keys.col.limit")}</Th>
          <Th className="hidden lg:table-cell">{t("keys.col.expires")}</Th>
          <Th className="hidden 2xl:table-cell">{t("keys.col.created")}</Th>
          {manage ? <Th className="w-0" /> : null}
        </tr>
      </thead>
      <tbody>
        {keys.map((key) => (
          <KeyRow key={key.id} apiKey={key} manage={manage} onEdit={onEdit} onDelete={onDelete} />
        ))}
      </tbody>
    </Table>
  );
}

function KeyStatus({ apiKey }: { apiKey: Key }) {
  const { t } = useTranslation();
  if (apiKey.expires_at !== undefined && new Date(apiKey.expires_at) <= new Date()) {
    return (
      <Badge tone="bad" icon="alert">
        {t("keys.expired")}
      </Badge>
    );
  }
  return (
    <Badge tone={apiKey.disabled ? "warn" : "ok"} icon={apiKey.disabled ? "alert" : "check"}>
      {apiKey.disabled ? t("keys.disabled") : t("keys.enabled")}
    </Badge>
  );
}

function KeyLimit({ apiKey }: { apiKey: Key }) {
  const { t } = useTranslation();
  const format = useFormat();
  if (!apiKey.spend_limit) {
    return t("keys.noLimit");
  }
  return (
    <>
      <p>{t("keys.limitLeft", { left: apiKey.limit_remaining ? format.money(apiKey.limit_remaining) : "—" })}</p>
      <p className="text-label text-t4">
        {t("keys.limitOf", { limit: format.money(apiKey.spend_limit) })}
        {apiKey.limit_reset ? ` · ${t(`keys.reset.${apiKey.limit_reset}`)}` : ""}
      </p>
    </>
  );
}

function KeyRow({
  apiKey,
  manage,
  onEdit,
  onDelete,
}: {
  apiKey: Key;
  manage: boolean;
  onEdit: (key: Key) => void;
  onDelete: (key: Key) => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <tr>
      <Td className="w-full max-w-0 sm:w-auto sm:max-w-64 sm:min-w-40">
        <p className="truncate text-t1" title={apiKey.name}>
          {apiKey.name}
        </p>
        <p className="truncate font-mono text-label text-t4">{maskedKey(apiKey)}</p>
      </Td>
      <Td>
        <KeyStatus apiKey={apiKey} />
      </Td>
      <Td className="hidden tabular-nums md:table-cell">
        <KeyLimit apiKey={apiKey} />
      </Td>
      <Td className="hidden whitespace-nowrap lg:table-cell">
        {apiKey.expires_at ? format.dateTime(new Date(apiKey.expires_at)) : t("keys.neverExpires")}
      </Td>
      <Td className="hidden whitespace-nowrap 2xl:table-cell">{format.dateTime(new Date(apiKey.created_at))}</Td>
      {manage ? (
        <Td className="whitespace-nowrap">
          <Button
            variant="ghost"
            size="icon"
            aria-label={t("keys.editNamed", { name: apiKey.name })}
            onClick={() => onEdit(apiKey)}
          >
            <Icon name="pencil" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            aria-label={t("keys.deleteNamed", { name: apiKey.name })}
            onClick={() => onDelete(apiKey)}
          >
            <Icon name="trash" />
          </Button>
        </Td>
      ) : null}
    </tr>
  );
}
