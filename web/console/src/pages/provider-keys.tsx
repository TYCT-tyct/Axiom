import { consoleApi, type Schemas, unwrap, unwrapEmpty } from "@axiom/api";
import { accountKey, can, useErrors, useSession } from "@axiom/app";
import Wide from "@axiom/boards/AxApp/regions/Byok";
import wideEn from "@axiom/boards/AxApp/regions/Byok.en";
import wideZh from "@axiom/boards/AxApp/regions/Byok.zh";
import Phone from "@axiom/boards/AxAppPhone/regions/Byok";
import phoneEn from "@axiom/boards/AxAppPhone/regions/Byok.en";
import phoneZh from "@axiom/boards/AxAppPhone/regions/Byok.zh";
import { Button, Dialog, FormError, Icon, SubmitButton, useFormat, usePageTitle } from "@axiom/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { type SyntheticEvent, useState } from "react";
import { useTranslation } from "react-i18next";
import { AddProviderKeyDialog } from "../provider-key-dialog";
import { providerKeysQuery } from "../queries";
import { ListStates, pressKey, useWide } from "./board";

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
  const [open, setOpen] = useState<ProviderKey | "add" | null>(null);
  const wide = useWide();
  const english = (i18n.resolvedLanguage ?? i18n.language).startsWith("en");
  usePageTitle(t("nav.providerKeys"));
  const rows = (keys.data?.data ?? []).map((key) => row(key, t));
  const values = {
    openKey: (event: SyntheticEvent) => setOpen(keyOf(keys.data?.data, event) ?? null),
    press: pressKey,
  };
  const text = { title: t("nav.providerKeys"), notice: t("providerKeys.notice") };
  const create = manage ? (
    <Button variant="primary" size="md" onClick={() => setOpen("add")}>
      <Icon name="plus" />
      {t("providerKeys.add")}
    </Button>
  ) : undefined;
  const description = t("providerKeys.description");
  return (
    <>
      <ListStates query={keys} allowed={view} ns="providerKeys" />
      {wide ? (
        <Wide
          t={(key) => (english ? wideEn : wideZh)[key]}
          values={values}
          text={text}
          rows={{ keys0: rows }}
          description={description}
          create={create}
        />
      ) : (
        <Phone
          t={(key) => (english ? phoneEn : phoneZh)[key]}
          values={values}
          text={text}
          rows={{ keys0: rows }}
          description={description}
          create={create}
        />
      )}
      {open === "add" ? <AddProviderKeyDialog onClose={() => setOpen(null)} /> : null}
      {open && open !== "add" ? <KeyDialog providerKey={open} manage={manage} onClose={() => setOpen(null)} /> : null}
    </>
  );
}

// keyOf is the row an event came from: every row carries its key's id.
function keyOf(keys: ProviderKey[] | undefined, event: SyntheticEvent) {
  const row = (event.currentTarget as HTMLElement).closest(".qrow") as HTMLElement | null;
  const id = row?.dataset.id;
  return keys?.find((key) => key.id === id);
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
  // The board sets a key's last four characters in its monospace face, where there is one.
  if (key.hint) {
    return { ...common, kind: "1", f7: "mono", f8: `•••• ${key.hint}`, f9: ` · ${fallback}` };
  }
  return { ...common, kind: "0", f7: [key.provider, state, fallback].join(" · ") };
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
      <div className="space-y-4">
        <dl className="space-y-1 text-label">
          <div className="flex gap-2">
            <dt>{t("providerKeys.provider")}</dt>
            <dd>{providerKey.provider}</dd>
          </div>
          <div className="flex gap-2">
            <dt>{t("providerKeys.status")}</dt>
            <dd>
              {providerKey.rejected
                ? t("providerKeys.rejected")
                : providerKey.enabled
                  ? t("keys.enabled")
                  : t("keys.disabled")}
            </dd>
          </div>
          <div className="flex gap-2">
            <dt>{t("providerKeys.fallback")}</dt>
            <dd>{providerKey.fallback ? t("providerKeys.fallbackOn") : t("providerKeys.noFallback")}</dd>
          </div>
          <div className="flex gap-2">
            <dt>{t("providerKeys.added")}</dt>
            <dd>{format.dateTime(new Date(providerKey.created_at))}</dd>
          </div>
        </dl>
        {tested ? <p role="status">{tested}</p> : null}
        {manage ? (
          <KeyActions providerKey={providerKey} onTested={setTested} onClose={onClose} />
        ) : (
          <p className="text-label text-t3">{t("providerKeys.readOnly")}</p>
        )}
      </div>
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
    <div className="space-y-3">
      <div className="flex flex-wrap gap-2">
        <Button size="sm" onClick={() => test.mutate()} disabled={test.isPending}>
          {test.isPending ? t("providerKeys.testing") : t("providerKeys.test.action")}
        </Button>
        <Button size="sm" onClick={() => toggle.mutate()} disabled={toggle.isPending || providerKey.rejected}>
          {providerKey.enabled ? t("providerKeys.disable") : t("providerKeys.enable")}
        </Button>
        <Button size="sm" variant="danger" onClick={() => setConfirming(true)}>
          {t("providerKeys.remove")}
        </Button>
      </div>
      {failure ? <FormError>{errors.message(failure)}</FormError> : null}
      {confirming ? (
        <Removal providerKey={providerKey} onCancel={() => setConfirming(false)} onClose={onClose} />
      ) : null}
    </div>
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
    <div className="space-y-2">
      <p className="text-t2 [overflow-wrap:anywhere]">{t("providerKeys.removeBody", { name: providerKey.name })}</p>
      {remove.isError ? <FormError>{errors.message(remove.error)}</FormError> : null}
      <div className="flex justify-end gap-2">
        <Button size="md" onClick={onCancel}>
          {t("cancel", { ns: "common" })}
        </Button>
        <SubmitButton pending={remove.isPending} variant="danger" onClick={() => remove.mutate()}>
          {t("providerKeys.confirmRemove")}
        </SubmitButton>
      </div>
    </div>
  );
}
