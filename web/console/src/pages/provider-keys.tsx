import { ApiError, consoleApi, type Schemas, unwrap, unwrapEmpty } from "@axiom/api";
import { accountKey, can, useErrors, useSession } from "@axiom/app";
import Wide from "@axiom/boards/AxApp/regions/Byok";
import wideEn from "@axiom/boards/AxApp/regions/Byok.en";
import wideZh from "@axiom/boards/AxApp/regions/Byok.zh";
import Phone from "@axiom/boards/AxAppPhone/regions/Byok";
import phoneEn from "@axiom/boards/AxAppPhone/regions/Byok.en";
import phoneZh from "@axiom/boards/AxAppPhone/regions/Byok.zh";
import {
  Button,
  Cluster,
  Dialog,
  FormError,
  Icon,
  PageFrame,
  SettingRow,
  Stack,
  SubmitButton,
  Text,
  useFormat,
  usePageTitle,
} from "@axiom/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { type ReactNode, type SyntheticEvent, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ListStates, pressKey, useWide } from "../board";
import { AddProviderKeyDialog } from "../provider-key-dialog";
import { providerKeysQuery } from "../queries";

type ProviderKey = Schemas["ProviderKey"];
type Props = Parameters<typeof Wide>[0];
type Row = Props["rows"]["keys0"][number];

// ProviderKeys is the board's BYOK page (#747): the account's own provider keys, each with its hint, whether routing may
// use it, and whether it may fall back to the site's endpoints for a model it covers (docs/domains/accounts.md「自带的
// 供应商 key（BYOK）」). A key is never shown again: the row keeps the last four characters only, and a row's name opens
// the key's test, switch and removal, which the design draws none of (console-gap L20).
export default function ProviderKeys() {
  const { t, i18n } = useTranslation();
  const session = useSession();
  const view = can(session, "api_keys.view");
  const manage = can(session, "api_keys.manage");
  const keys = useQuery({ ...providerKeysQuery, enabled: view });
  // The dialog shows the key as the list now has it, not as it was when it was opened: a switch, a test or another tab's
  // change moves it, and a key that is gone (removed here or elsewhere) closes its dialog.
  const [open, setOpen] = useState<"add" | string | null>(null);
  const wide = useWide();
  const english = (i18n.resolvedLanguage ?? i18n.language).startsWith("en");
  usePageTitle(t("nav.providerKeys"));
  // A session that loses `api_keys.view`, or a node that refuses the read, keeps what the query cache holds: the page
  // shows its states instead of the rows, offers nothing to make, and closes whatever dialog was open. A read that
  // merely failed keeps the page as it is — a draft stays — and the list offers a retry.
  const refused = keys.error instanceof ApiError && keys.error.status === 403;
  const permitted = view && !refused;
  const list = permitted && !keys.isError ? (keys.data?.data ?? []) : [];
  const opened = open && open !== "add" ? list.find((key) => key.id === open) : undefined;
  useEffect(() => {
    if (!permitted) {
      setOpen(null);
    }
  }, [permitted]);
  const closeKey = (id: string) => setOpen((current) => (current === id ? null : current));
  const closeAdd = () => setOpen((current) => (current === "add" ? null : current));
  const rows = list.map((key) => row(key, t));
  const values = {
    openKey: (event: SyntheticEvent) => setOpen(keyOf(list, event) ?? null),
    press: pressKey,
  };
  const text = { title: t("nav.providerKeys"), notice: t("providerKeys.notice") };
  const create =
    manage && permitted ? (
      <Button variant="primary" size="md" onClick={() => setOpen("add")}>
        <Icon name="plus" />
        {t("providerKeys.add")}
      </Button>
    ) : undefined;
  const description = t("providerKeys.description");
  return (
    <PageFrame>
      <Board
        wide={wide}
        english={english}
        values={values}
        text={text}
        rows={rows}
        description={description}
        create={create}
      />
      <ListStates query={keys} view={view} ns="providerKeys" />
      {permitted ? (
        <Opened
          open={open}
          key0={opened}
          manage={manage}
          onClose={(id) => (id === "add" ? closeAdd() : closeKey(id))}
        />
      ) : null}
    </PageFrame>
  );
}

// Board is the page in the board's own form: the wide board from 1024px, the phone's below.
function Board({
  wide,
  english,
  values,
  text,
  rows,
  description,
  create,
}: {
  wide: boolean;
  english: boolean;
  values: Parameters<typeof Wide>[0]["values"];
  text: Parameters<typeof Wide>[0]["text"];
  rows: Props["rows"]["keys0"];
  description: string;
  create: ReactNode;
}) {
  const text_ = (en: Record<string, string>, zh: Record<string, string>) => (key: string) =>
    (english ? en : zh)[key] as string;
  return wide ? (
    <Wide
      t={text_(wideEn, wideZh) as never}
      values={values}
      text={text}
      rows={{ keys0: rows }}
      description={description}
      create={create}
    />
  ) : (
    <Phone
      t={text_(phoneEn, phoneZh) as never}
      values={values}
      text={text}
      rows={{ keys0: rows }}
      description={description}
      create={create}
    />
  );
}

// Opened is the add dialog or the key a row's name opened, always as the list now has that key; the board draws neither
// (console-gap L20).
function Opened({
  open,
  key0,
  manage,
  onClose,
}: {
  open: "add" | string | null;
  key0: ProviderKey | undefined;
  manage: boolean;
  onClose: (id: "add" | string) => void;
}) {
  if (open === "add") {
    return <AddProviderKeyDialog onClose={() => onClose("add")} />;
  }
  return key0 ? <KeyDialog providerKey={key0} manage={manage} onClose={() => onClose(key0.id)} /> : null;
}

// keyOf is the key whose row an event came from: every row carries its key's id.
function keyOf(keys: ProviderKey[], event: SyntheticEvent) {
  const id = ((event.currentTarget as HTMLElement).closest(".qrow") as HTMLElement | null)?.dataset.id;
  return keys.find((key) => key.id === id)?.id;
}

// row is one provider key as the board draws it: its name, and a line with the provider, the last four characters, how
// routing sees it, and whether it may fall back to the site's endpoints.
function row(key: ProviderKey, t: TFunction): Row {
  const state = key.rejected ? t("providerKeys.rejected") : key.enabled ? t("keys.enabled") : t("keys.disabled");
  const fallback = key.fallback ? t("providerKeys.fallbackOn") : t("providerKeys.noFallback");
  const common = {
    key: key.id,
    f0: "qrow",
    f1: key.id,
    f2: "qrl",
    f3: "button",
    f4: "0",
    f5: t("providerKeys.openNamed", { name: key.name }),
    f6: key.name,
  };
  // The board sets a key's last four characters in its monospace face where there is one; the rest of the line, which
  // says the provider, how routing sees the key and its fallback, is the same either way.
  const rest = [key.provider, state, fallback];
  if (key.hint) {
    return { ...common, kind: "1", f7: "mono", f8: `•••• ${key.hint}`, f9: ` · ${rest.join(" · ")}` };
  }
  return { ...common, kind: "0", f7: rest.join(" · ") };
}

// KeyDialog is a provider key's own page in a dialog: what the key is, whether the provider takes it, its switch, its
// fallback, and removing it. The design draws only an add button and a "prefer" switch (console-gap L20).
function KeyDialog({
  providerKey,
  manage,
  onClose,
}: {
  providerKey: ProviderKey;
  manage: boolean;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  const [tested, setTested] = useState<string>();
  return (
    <Dialog title={providerKey.name} onClose={onClose}>
      <Stack>
        <div>
          <SettingRow title={t("providerKeys.provider")}>{providerKey.provider}</SettingRow>
          <SettingRow title={t("providerKeys.status")}>
            {providerKey.rejected
              ? t("providerKeys.rejected")
              : providerKey.enabled
                ? t("keys.enabled")
                : t("keys.disabled")}
          </SettingRow>
          <SettingRow title={t("providerKeys.fallback")}>
            {providerKey.fallback ? t("providerKeys.fallbackOn") : t("providerKeys.noFallback")}
          </SettingRow>
          <SettingRow title={t("providerKeys.added")}>{format.dateTime(new Date(providerKey.created_at))}</SettingRow>
        </div>
        {tested ? <p role="status">{tested}</p> : null}
        {manage ? (
          <KeyActions providerKey={providerKey} onTested={setTested} onClose={onClose} />
        ) : (
          <Text size="sm" tone="faint">
            {t("providerKeys.readOnly")}
          </Text>
        )}
      </Stack>
    </Dialog>
  );
}

// KeyActions tests the key and switches it; removing one has its own confirmation, since the sealed value is gone for
// good. A test that fails says whether the provider refused the key or could not be asked: the node keeps the
// provider's own words out of the account's page.
function KeyActions({
  providerKey,
  onTested,
  onClose,
}: {
  providerKey: ProviderKey;
  onTested: (text: string) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const refresh = () => queryClient.invalidateQueries({ queryKey: [accountKey, "provider-keys"] });
  const test = useMutation({
    mutationFn: () =>
      unwrap(
        consoleApi.POST("/api/console/v1/provider-keys/{provider_key_id}/test", {
          params: { path: { provider_key_id: providerKey.id } },
        }),
      ),
    onSuccess: async (result) => {
      // The list first, so the line the dialog shows and the message about the test agree: a key the provider refused and
      // then accepted must not read as both.
      await refresh();
      onTested(
        result.inconclusive
          ? t("providerKeys.test.inconclusive")
          : result.ok
            ? t("providerKeys.test.ok")
            : t("providerKeys.test.refused"),
      );
    },
  });
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
  const failure = test.error ?? toggle.error;
  return (
    <Stack gap={12}>
      <Cluster>
        <Button size="sm" onClick={() => test.mutate()} disabled={test.isPending}>
          {test.isPending ? t("providerKeys.testing") : t("providerKeys.test.action")}
        </Button>
        <Button size="sm" onClick={() => toggle.mutate()} disabled={toggle.isPending || providerKey.rejected}>
          {providerKey.enabled ? t("providerKeys.disable") : t("providerKeys.enable")}
        </Button>
        <Button size="sm" variant="danger" onClick={() => setConfirming(true)}>
          {t("providerKeys.remove")}
        </Button>
      </Cluster>
      {failure ? <FormError>{errors.message(failure)}</FormError> : null}
      {confirming ? (
        <Removal providerKey={providerKey} onCancel={() => setConfirming(false)} onClose={onClose} />
      ) : null}
    </Stack>
  );
}

// Removal erases the key once the user confirms: calls already made keep their rows.
function Removal({
  providerKey,
  onCancel,
  onClose,
}: {
  providerKey: ProviderKey;
  onCancel: () => void;
  onClose: () => void;
}) {
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
    <Stack gap={8}>
      <Text tone="muted">{t("providerKeys.removeBody", { name: providerKey.name })}</Text>
      {remove.isError ? <FormError>{errors.message(remove.error)}</FormError> : null}
      <Cluster justify="end">
        <Button size="md" onClick={onCancel}>
          {t("cancel", { ns: "common" })}
        </Button>
        <SubmitButton pending={remove.isPending} variant="danger" onClick={() => remove.mutate()}>
          {t("providerKeys.confirmRemove")}
        </SubmitButton>
      </Cluster>
    </Stack>
  );
}
