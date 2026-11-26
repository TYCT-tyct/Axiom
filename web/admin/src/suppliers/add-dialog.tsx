import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { ChoiceCards, Field, SegmentedControl, Select, Text, TextArea, TextLink } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, isProblem, type Problem } from "../change";
import { suppliersQuery } from "../queries";
import { ConnectDialog } from "./connect";
import { Checks, KeysField, LimitFields, readKeys, readLimits, readRegions, validRegion } from "./key-fields";
import { CloudCredentialFields, readCloudCredential } from "./supplier-dialogs";
import { modeOf } from "./upstream";

type Way = "relay" | "key" | "cloud";
type Provider = AdminSchemas["DirectProvider"];
type Cloud = AdminSchemas["CloudProvider"];
type Check = AdminSchemas["CredentialCheck"];
type T = ReturnType<typeof useTranslation>["t"];

// Where a built-in provider answers, as the page says it (docs/domains/suppliers-direct.md); its format and price list
// come with it, so only the keys are asked for.
const origins: Record<Exclude<Provider, "compatible">, string> = {
  openai: "https://api.openai.com",
  anthropic: "https://api.anthropic.com",
  google: "https://generativelanguage.googleapis.com",
  moonshot: "https://api.moonshot.cn",
};
const providers = ["openai", "anthropic", "google", "moonshot", "compatible"] as const satisfies readonly Provider[];
const clouds = ["vertex", "bedrock", "azure"] as const satisfies readonly Cloud[];
// The Gemini format is not sent yet (suppliers-direct.md「决策与理由」), so a compatible API is OpenAI's or Anthropic's.
const formats = ["openai", "anthropic"] as const satisfies readonly AdminSchemas["ApiFormat"][];
const origin = /^https:\/\/[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$/;

// What a way of adding ends in: what each key or region came to, and the supplier, when one was stored.
type Added = { checks: Check[]; supplierId?: string };

// 添加上游账号 (adA_upkNew): one dialog for every way in, chosen by the cards at its top. A relay is connected by
// signing in to it (connect.tsx, unchanged); keys are pasted for a provider's own API, new or connected already; a cloud
// platform takes its credential. Every key and region is checked before it is kept, and what each came to is listed;
// closing the result opens the supplier that was made.
export default function AddUpstream({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [way, setWay] = useState<Way>("relay");
  const [made, setMade] = useState<string>();
  const close = () => {
    onClose();
    if (made) void navigate({ to: "/suppliers/$supplierId", params: { supplierId: made } });
  };
  const cards = (
    <ChoiceCards
      label={t("upstream.addDialog.ways")}
      value={way}
      onChange={setWay}
      choices={[
        { value: "relay", icon: "link", ...words(t, "relay") },
        { value: "key", icon: "keys", ...words(t, "key") },
        { value: "cloud", icon: "cloud", ...words(t, "cloud") },
      ]}
    />
  );
  if (way === "relay") return <ConnectDialog header={cards} onClose={onClose} />;
  const Form = way === "key" ? KeysWay : CloudWay;
  return <Form key={way} cards={cards} onMade={setMade} onClose={close} />;
}

const words = (t: T, way: Way) => ({
  title: t(`upstream.addDialog.${way}.title`),
  detail: t(`upstream.addDialog.${way}.detail`),
});

function Result({ added }: { added: Added }) {
  const { t } = useTranslation();
  const passed = added.checks.filter((check) => check.outcome === "passed").length;
  return (
    <>
      <Text medium>{passed > 0 ? t("upstream.addDialog.added", { count: passed }) : t("upstream.addDialog.none")}</Text>
      <Checks checks={added.checks} />
      <Text tone="muted">{passed > 0 ? t("upstream.addDialog.next") : t("upstream.addDialog.noneBody")}</Text>
      {added.supplierId && passed > 0 ? (
        <TextLink>
          <Link to="/suppliers/$supplierId" params={{ supplierId: added.supplierId }}>
            {t("upstream.addDialog.open")}
          </Link>
        </TextLink>
      ) : null}
    </>
  );
}

type WayProps = { cards: React.ReactNode; onMade: (id: string) => void; onClose: () => void };

type DirectInput =
  | { to: string; body: Omit<AdminSchemas["SupplierKeysAdd"], "reason"> }
  | { to: null; body: Omit<AdminSchemas["DirectSupplierCreate"], "reason"> };

// 贴 Key: to a direct API connected already, or to a new one of a built-in provider (only its keys asked for) or any
// other compatible API (its name, address, format and currency too).
function KeysWay({ cards, onMade, onClose }: WayProps) {
  const { t } = useTranslation();
  const list = useInfiniteQuery(suppliersQuery());
  const direct = (list.data?.pages.flatMap((page) => page.data) ?? []).filter((s) => modeOf(s) === "direct");
  const [to, setTo] = useState("");
  const [provider, setProvider] = useState<Provider>("openai");
  const read = (values: Record<string, string>): DirectInput | Problem => {
    const keys = readKeys(values, t);
    const limits = readLimits(values, t);
    if (isProblem(keys)) return keys;
    if (isProblem(limits)) return limits;
    if (to !== "") return { to, body: { keys, ...limits } };
    return readNewDirect(values, provider, t, { keys, ...limits });
  };
  return (
    <ChangeDialog
      operation="supplier-direct"
      {...wayHead(t, cards)}
      fields={(error) => (
        <>
          <Select label={t("upstream.addDialog.to")} name="to" value={to} onChange={(e) => setTo(e.target.value)}>
            {direct.map((supplier) => (
              <option key={supplier.id} value={supplier.id}>
                {supplier.name}
              </option>
            ))}
            <option value="">{t("upstream.addDialog.newApi")}</option>
          </Select>
          {to === "" ? <NewDirectFields provider={provider} onProvider={setProvider} error={error} /> : null}
          <KeysField error={error} />
          <LimitFields error={error} />
        </>
      )}
      read={read}
      send={(input, { reason, key }) => sendDirect(input, reason, key, onMade)}
      done={(added) => <Result added={added} />}
      onClose={onClose}
    />
  );
}

function readNewDirect(
  values: Record<string, string>,
  provider: Provider,
  t: T,
  keys: { keys: string[]; max_concurrency: number; requests_per_minute?: number },
): DirectInput | Problem {
  const p = (name: "name" | "origin" | "currency") => ({ problem: t(`upstream.addDialog.problems.${name}`) });
  if (provider !== "compatible") {
    const name = t("upstream.addDialog.officialName", { provider: t(`upstream.addDialog.providers.${provider}`) });
    return { to: null, body: { provider, name, ...keys } };
  }
  const name = (values.name ?? "").trim();
  const at = (values.origin ?? "").trim().replace(/\/+$/, "");
  const currency = (values.settlement_currency ?? "").trim().toUpperCase();
  if (!name) return p("name");
  if (!origin.test(at)) return p("origin");
  if (!/^[A-Z]{3}$/.test(currency)) return p("currency");
  const format = values.format === "anthropic" ? "anthropic" : "openai";
  return { to: null, body: { provider, name, origin: at, format, settlement_currency: currency, ...keys } };
}

async function sendDirect(input: DirectInput, reason: string, key: string, onMade: (id: string) => void) {
  const header = { "Idempotency-Key": key };
  if (input.to !== null) {
    const sent = await unwrapChange(
      adminApi.POST("/api/admin/v1/suppliers/{supplier_id}/keys", {
        params: { path: { supplier_id: input.to }, header },
        body: { ...input.body, reason },
      }),
    );
    return { ...sent, data: { checks: sent.data.data, supplierId: input.to } };
  }
  const sent = await unwrapChange(
    adminApi.POST("/api/admin/v1/suppliers/direct", { params: { header }, body: { ...input.body, reason } }),
  );
  return connected(sent, onMade);
}

// A new supplier's answer: what each key or region came to, and the supplier, which closing the result opens.
function connected<S extends { data: AdminSchemas["SupplierConnected"] }>(sent: S, onMade: (id: string) => void) {
  if (sent.data.supplier) onMade(sent.data.supplier.id);
  return { ...sent, data: { checks: sent.data.checks, supplierId: sent.data.supplier?.id } };
}

// What both ways of pasting share at the top of their dialog: the cards, the line under them and the button.
function wayHead(t: T, cards: React.ReactNode) {
  return {
    title: t("upstream.add"),
    intro: (
      <>
        {cards}
        <p>{t("upstream.addDialog.lede")}</p>
      </>
    ),
    action: t("upstream.addDialog.action"),
    verdict: true,
  };
}

function NewDirectFields({
  provider,
  onProvider,
  error,
}: {
  provider: Provider;
  onProvider: (provider: Provider) => void;
  error: unknown;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <Select
        label={t("upstream.addDialog.provider")}
        hint={provider === "compatible" ? undefined : t("upstream.addDialog.builtIn", { origin: origins[provider] })}
        name="provider"
        value={provider}
        onChange={(event) => onProvider(event.target.value as Provider)}
        options={providers.map((value) => ({ value, label: t(`upstream.addDialog.providers.${value}`) }))}
      />
      {provider === "compatible" ? (
        <>
          <Field
            label={t("upstream.addDialog.name")}
            placeholder={t("upstream.addDialog.namePlaceholder")}
            name="name"
            maxLength={200}
            autoComplete="off"
            error={errors.field(error, "name")}
          />
          <Field
            label={t("upstream.addDialog.origin")}
            hint={t("upstream.addDialog.originHint")}
            name="origin"
            type="url"
            inputMode="url"
            placeholder="https://api.example.com"
            autoComplete="off"
            spellCheck={false}
            maxLength={255}
            error={errors.field(error, "origin")}
          />
          <Select
            label={t("upstream.addDialog.format")}
            name="format"
            defaultValue="openai"
            options={formats.map((value) => ({ value, label: t(`upstream.addDialog.formats.${value}`) }))}
          />
          <Field
            label={t("upstream.addDialog.currency")}
            hint={t("upstream.addDialog.currencyHint")}
            name="settlement_currency"
            defaultValue="USD"
            maxLength={3}
            autoComplete="off"
            spellCheck={false}
            autoCapitalize="characters"
            error={errors.field(error, "settlement_currency")}
          />
        </>
      ) : null}
    </>
  );
}

type CloudInput = Omit<AdminSchemas["CloudSupplierCreate"], "reason">;

function readCloud(provider: Cloud, values: Record<string, string>, t: T): CloudInput | Problem {
  const name = (values.name ?? "").trim();
  if (!name) return { problem: t("upstream.addDialog.problems.name") };
  const credential = readCloudCredential(provider, values, t);
  if (isProblem(credential)) return credential;
  const limits = readLimits(values, t);
  if (isProblem(limits)) return limits;
  if (provider === "azure") {
    const at = (values.origin ?? "").trim().replace(/\/+$/, "");
    return origin.test(at) && /\.azure\.com$/.test(at)
      ? { provider, name, origin: at, credential, ...limits }
      : { problem: t("upstream.addDialog.problems.resource") };
  }
  const regions = readRegions(values);
  return regions.length > 0 && regions.length <= 20 && regions.every(validRegion)
    ? { provider, name, credential, regions, ...limits }
    : { problem: t("upstream.addDialog.problems.regions") };
}

// 云厂商: Vertex AI's service account key, Bedrock's access key, or an Azure OpenAI resource and its key; the first
// two are checked and limited by each region chosen.
function CloudWay({ cards, onMade, onClose }: WayProps) {
  const { t } = useTranslation();
  const [provider, setProvider] = useState<Cloud>("vertex");
  const read = (values: Record<string, string>) => readCloud(provider, values, t);
  return (
    <ChangeDialog
      operation="supplier-cloud"
      {...wayHead(t, cards)}
      fields={(error) => <CloudFields provider={provider} onProvider={setProvider} error={error} />}
      read={read}
      send={async (body, { reason, key }) => {
        const sent = await unwrapChange(
          adminApi.POST("/api/admin/v1/suppliers/cloud", {
            params: { header: { "Idempotency-Key": key } },
            body: { ...body, reason },
          }),
        );
        return connected(sent, onMade);
      }}
      done={(added: Added) => <Result added={added} />}
      onClose={onClose}
    />
  );
}

// A cloud platform's fields: which one, its name, an Azure resource's address, its credential, and the regions of
// the two that serve by region.
function CloudFields({
  provider,
  onProvider,
  error,
}: {
  provider: Cloud;
  onProvider: (provider: Cloud) => void;
  error: unknown;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <SegmentedControl
        label={t("upstream.addDialog.cloudProvider")}
        value={provider}
        options={clouds.map((value) => ({ value, label: t(`upstream.addDialog.clouds.${value}`) }))}
        onChange={onProvider}
      />
      <Field
        label={t("upstream.addDialog.name")}
        placeholder={t("upstream.addDialog.namePlaceholder")}
        name="name"
        defaultValue={t(`upstream.addDialog.clouds.${provider}`)}
        maxLength={200}
        autoComplete="off"
        error={errors.field(error, "name")}
      />
      {provider === "azure" ? (
        <Field
          label={t("upstream.addDialog.resource")}
          name="origin"
          type="url"
          placeholder={t("upstream.addDialog.resourcePlaceholder")}
          autoComplete="off"
          spellCheck={false}
          maxLength={255}
          error={errors.field(error, "origin")}
        />
      ) : null}
      <CloudCredentialFields provider={provider} error={error} />
      {provider === "azure" ? null : (
        <TextArea
          label={t("upstream.addDialog.regions")}
          hint={t("upstream.addDialog.regionsHint")}
          name="regions"
          rows={2}
          defaultValue={provider === "vertex" ? "us-east5" : "us-east-1"}
          spellCheck={false}
          error={errors.field(error, "regions")}
        />
      )}
      <LimitFields error={error} />
    </>
  );
}
