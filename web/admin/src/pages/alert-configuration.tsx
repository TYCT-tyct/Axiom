import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import {
  ActionButton,
  DataState,
  EmptyState,
  Field,
  moneyInput,
  PageHead,
  parseMoney,
  Select,
  SettingRow,
  Stack,
  Tag,
  Text,
  TextArea,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, useSnapshot } from "../change";
import en from "../locales/en/alert-configuration";
import settingWords from "../locales/settings";
import zh from "../locales/zh/alert-configuration";
import { formatJSON, parseJSONLossless } from "../lossless";
import { Code, PageBody } from "../parts";
import { alertConfigurationQuery } from "../policy-queries";
import { suppliersQuery } from "../queries";
import { type NumberSpec, numberOf, numberText, numberValid } from "../setting-fields";
import { NumberSetting, SettingsCard, SwitchSetting } from "../setting-rows";
import { SettingsSubmit, type SettingValue, useSettingDrafts } from "../settings-drafts";

// The rows of the spend detection are settings, so the page carries their words too.
export const messages = {
  zh: { alertConfig: zh, ...settingWords.zh },
  en: { alertConfig: en, ...settingWords.en },
};

type Config = AdminSchemas["AlertConfiguration"];
type Spend = AdminSchemas["SpendDetection"];
type ConfigInput = Omit<AdminSchemas["AlertConfigurationRequest"], "reason">;
type Channel = AdminSchemas["AlertChannel"];
type Rule = AdminSchemas["AlertRule"];
type Line = AdminSchemas["LowBalanceThreshold"];
type Edit = { kind: "configuration" | "add" } | { kind: "test"; id: string } | { kind: "secret"; channel: Channel };

// The channel kinds, each named by the one destination member it sets (api/admin.yaml AlertChannel), and the secret it
// takes: a webhook's signing secret rotates with an hour's overlap, a bot's replaces the old one at once.
const kinds = {
  email: { field: "email", secret: undefined },
  webhook: { field: "webhook_url", secret: "rotate" },
  feishu: { field: "feishu_url", secret: "botSecret" },
  wecom: { field: "wecom_url", secret: undefined },
  telegram: { field: "telegram_chat_id", secret: "botToken" },
} as const satisfies Record<string, { field: keyof Channel; secret?: "rotate" | "botSecret" | "botToken" }>;
type Kind = keyof typeof kinds;

// putConfiguration replaces the rules and channels, every form of this page sending the whole configuration it read.
const putConfiguration = (body: ConfigInput, { key, reason }: { key: string; reason: string }) =>
  unwrapChange(
    adminApi.PUT("/api/admin/v1/alerts/configuration", {
      params: { header: { "Idempotency-Key": key } },
      body: { ...body, reason },
      bodySerializer: formatJSON,
    }),
  );

// kindOf is the channel's kind and destination: the first destination member it sets.
function kindOf(channel: Channel): [Kind, string] | undefined {
  for (const [kind, { field }] of Object.entries(kinds) as [Kind, (typeof kinds)[Kind]][]) {
    const value = channel[field];
    if (typeof value === "string" && value) return [kind, value];
  }
  return undefined;
}

// The spend detection's numbers with the bounds of api/admin.yaml (SpendDetection) in the API's own unit and the unit the
// design writes them in; the floor is billionths of a credit, an int64 kept as written.
const spendTimes = {
  "alerts.spendWindow": { name: "window_seconds", min: 900, max: 86_400, unit: "minute" },
  "alerts.spendBaseline": { name: "baseline_seconds", min: 900, max: 604_800, unit: "hour" },
} as const satisfies Record<string, NumberSpec>;
const spendCounts = {
  "alerts.spendRegion": { name: "min_new_region_calls", min: 1, max: 1_000_000 },
  "alerts.spendModel": { name: "min_new_model_calls", min: 1, max: 1_000_000 },
} as const satisfies Record<string, NumberSpec>;
const floorMax = 1_000_000_000_000_000_000n;

type Key =
  | "alerts.spendOn"
  | "alerts.spendNotify"
  | "alerts.spendRatio"
  | "alerts.spendMin"
  | keyof typeof spendTimes
  | keyof typeof spendCounts;

export default function AlertConfiguration() {
  const { t } = useTranslation();
  const query = useQuery(alertConfigurationQuery);
  if (query.data === undefined || query.data.rev === undefined) {
    return (
      <PageBody>
        <PageHead title={t("shell.nav.alertRules")} lede={t("alertConfig.description")} />
        <DataState
          query={query}
          isEmpty={(view) => view.rev === undefined}
          empty={<EmptyState title={t("alertConfig.unavailable")} />}
        >
          {() => null}
        </DataState>
      </PageBody>
    );
  }
  return <Configuration view={query.data} />;
}

// The floor's text: billionths written as credits.
const floorText = (spend: Spend) => moneyInput(String(spend.min_spend_nano));

function savedOf(spend: Spend | undefined): Record<Key, SettingValue> {
  return {
    "alerts.spendOn": spend?.enabled === true,
    "alerts.spendNotify": spend?.notify_users === true,
    "alerts.spendWindow": numberText(spendTimes["alerts.spendWindow"], spend?.window_seconds),
    "alerts.spendBaseline": numberText(spendTimes["alerts.spendBaseline"], spend?.baseline_seconds),
    "alerts.spendRatio": spend ? String(spend.spend_ratio) : "",
    "alerts.spendMin": spend ? floorText(spend) : "",
    "alerts.spendRegion": numberText(spendCounts["alerts.spendRegion"], spend?.min_new_region_calls),
    "alerts.spendModel": numberText(spendCounts["alerts.spendModel"], spend?.min_new_model_calls),
  };
}

const ratioOf = (text: string) => (/^\d+(\.\d+)?$/.test(text.trim()) ? Number(text) : undefined);

function valid(key: Key, value: SettingValue): boolean {
  if (typeof value !== "string") return true;
  if (key === "alerts.spendRatio") return (ratioOf(value) ?? 0) >= 1 && (ratioOf(value) ?? 0) <= 1000;
  if (key === "alerts.spendMin") {
    const nano = parseMoney(value);
    return nano !== null && BigInt(nano) <= floorMax;
  }
  return numberValid(
    key in spendTimes ? spendTimes[key as keyof typeof spendTimes] : spendCounts[key as keyof typeof spendCounts],
    value,
    false,
  );
}

// spendOf is the detection as the rows say it: what the server holds, with the rows' values on top. The floor is written
// back as the number it was typed as, not through a float.
function spendOf(held: Spend, values: Record<Key, SettingValue>): Spend {
  const nano = parseMoney(String(values["alerts.spendMin"])) ?? "0";
  return {
    ...held,
    enabled: values["alerts.spendOn"] === true,
    notify_users: values["alerts.spendNotify"] === true,
    window_seconds:
      numberOf(spendTimes["alerts.spendWindow"], String(values["alerts.spendWindow"])) ?? held.window_seconds,
    baseline_seconds:
      numberOf(spendTimes["alerts.spendBaseline"], String(values["alerts.spendBaseline"])) ?? held.baseline_seconds,
    spend_ratio: ratioOf(String(values["alerts.spendRatio"])) ?? held.spend_ratio,
    min_spend_nano: parseJSONLossless(nano) as number,
    min_new_region_calls:
      numberOf(spendCounts["alerts.spendRegion"], String(values["alerts.spendRegion"])) ?? held.min_new_region_calls,
    min_new_model_calls:
      numberOf(spendCounts["alerts.spendModel"], String(values["alerts.spendModel"])) ?? held.min_new_model_calls,
  };
}

function useSpendDrafts(view: Config) {
  const { t } = useTranslation();
  const spend = view.spend_detection;
  return useSettingDrafts<Key>({
    scope: "settings:alerts",
    saved: savedOf(spend),
    check: valid,
    show: (key, value) => (key === "alerts.spendMin" ? `${value} ${t("unit.credits")}` : undefined),
    writes: {
      setAlertConfiguration: (values, { reason, key }) => {
        const body = { rev: view.rev ?? 1, rules: view.rules ?? [], channels: view.channels ?? [] } as ConfigInput;
        if (spend) body.spend_detection = spendOf(spend, values);
        return putConfiguration(body, { key, reason });
      },
    },
  });
}

function Configuration({ view }: { view: Config }) {
  const { t } = useTranslation();
  const [edit, setEdit] = useState<Edit>();
  const close = () => setEdit(undefined);
  const spend = view.spend_detection;
  const drafts = useSpendDrafts(view);
  return (
    <>
      <PageBody>
        <PageHead
          title={t("shell.nav.alertRules")}
          lede={t("alertConfig.description")}
          actions={
            <ActionButton asChild>
              <Link to="/alert-events">{t("alertConfig.seeEvents")}</Link>
            </ActionButton>
          }
        />
        <SettingsCard
          scope="platform"
          title={t("alertConfig.rules")}
          actions={
            <ActionButton size="sm" icon="pencil" onClick={() => setEdit({ kind: "configuration" })}>
              {t("alertConfig.edit")}
            </ActionButton>
          }
        >
          <Rules rules={view.rules ?? []} />
        </SettingsCard>
        <ChannelsCard view={view} edit={setEdit} />
        <SettingsCard
          scope="platform"
          title={t("alertConfig.balanceLines")}
          actions={
            <ActionButton size="sm" icon="pencil" onClick={() => setEdit({ kind: "configuration" })}>
              {t("alertConfig.edit")}
            </ActionButton>
          }
        >
          <BalanceLines lines={view.low_balance_thresholds ?? []} />
        </SettingsCard>
        {spend ? (
          <SettingsCard scope="platform" title={t("alertConfig.spend")}>
            <SwitchSetting drafts={drafts} setting="alerts.spendOn" />
            <SwitchSetting
              drafts={drafts}
              setting="alerts.spendNotify"
              disabled={drafts.value("alerts.spendOn") !== true}
            />
            <NumberSetting drafts={drafts} setting="alerts.spendWindow" unit={t("unit.minute")} />
            <NumberSetting drafts={drafts} setting="alerts.spendBaseline" unit={t("unit.hour")} />
            <NumberSetting drafts={drafts} setting="alerts.spendRatio" unit={t("unit.ratio")} decimals={1} />
            <NumberSetting drafts={drafts} setting="alerts.spendMin" unit={t("unit.credits")} />
            <NumberSetting drafts={drafts} setting="alerts.spendRegion" unit={t("unit.times")} />
            <NumberSetting drafts={drafts} setting="alerts.spendModel" unit={t("unit.times")} />
          </SettingsCard>
        ) : null}
        <SettingsSubmit drafts={drafts} />
      </PageBody>
      {edit?.kind === "configuration" ? <Replace live={view} onClose={close} /> : null}
      {edit?.kind === "test" ? <TestChannel id={edit.id} onClose={close} /> : null}
      {edit?.kind === "add" ? <AddChannel live={view} onClose={close} /> : null}
      {edit?.kind === "secret" ? <Rotate channel={edit.channel} live={view} onClose={close} /> : null}
    </>
  );
}

function Rules({ rules }: { rules: Rule[] }) {
  const { t } = useTranslation();
  const format = useFormat();
  if (!rules.length) return <EmptyState compact title={t("alertConfig.noRules")} />;
  return (
    <>
      {rules.map((rule) => (
        <SettingRow
          key={rule.id}
          title={rule.kind ?? rule.id ?? ""}
          detail={t("alertConfig.ruleSummary", {
            threshold: format.number(rule.threshold ?? 1),
            seconds: format.number(rule.duration_seconds ?? 0),
            channel: rule.channel || t("alertConfig.noChannel"),
          })}
        >
          <Tag
            tone={
              rule.minimum_severity === "critical" ? "bad" : rule.minimum_severity === "warning" ? "warn" : "neutral"
            }
          >
            {t(`alertConfig.severity.${rule.minimum_severity ?? "info"}`)}
          </Tag>
        </SettingRow>
      ))}
    </>
  );
}

// BalanceLines names each supplier a line is set for by the first page of suppliers, and by its ID past it.
function BalanceLines({ lines }: { lines: Line[] }) {
  const { t } = useTranslation();
  const format = useFormat();
  const suppliers = useInfiniteQuery(suppliersQuery());
  const names = new Map((suppliers.data?.pages ?? []).flatMap((page) => page.data ?? []).map((s) => [s.id, s.name]));
  return (
    <>
      {lines.map((line) => (
        <SettingRow
          key={line.supplier_id}
          title={names.get(line.supplier_id) ?? line.supplier_id}
          detail={t("alertConfig.balanceLine")}
        >
          <Code>{format.decimal(line.threshold)}</Code>
        </SettingRow>
      ))}
      <Text size="sm" tone="faint">
        {t("alertConfig.balanceDefault")}
      </Text>
    </>
  );
}

function ChannelsCard({ view, edit }: { view: Config; edit: (value: Edit) => void }) {
  const { t } = useTranslation();
  return (
    <SettingsCard
      scope="platform"
      title={t("alertConfig.channels")}
      actions={
        <ActionButton size="sm" icon="plus" onClick={() => edit({ kind: "add" })}>
          {t("alertConfig.add")}
        </ActionButton>
      }
    >
      <Channels channels={view.channels ?? []} edit={edit} />
      {view.admin_origin ? (
        <Text size="sm" tone="faint">
          {t("alertConfig.links", { origin: view.admin_origin })}
        </Text>
      ) : null}
    </SettingsCard>
  );
}

function Channels({ channels, edit }: { channels: Channel[]; edit: (value: Edit) => void }) {
  const { t } = useTranslation();
  if (!channels.length) return <EmptyState compact title={t("alertConfig.noChannels")} />;
  return (
    <>
      {channels.map((channel) => {
        if (!channel.id) return null;
        const [kind, destination] = kindOf(channel) ?? [undefined, "—"];
        const secret = kind ? kinds[kind].secret : undefined;
        return (
          <SettingRow
            key={channel.id}
            title={channel.id}
            detail={
              <>
                {kind ? <Tag>{t(`alertConfig.kind.${kind}`)}</Tag> : null} <Code>{destination}</Code>
              </>
            }
          >
            <ActionButton size="sm" onClick={() => edit({ kind: "test", id: channel.id ?? "" })}>
              {t("alertConfig.testChannel")}
            </ActionButton>
            {secret ? (
              <ActionButton size="sm" onClick={() => edit({ kind: "secret", channel })}>
                {t(`alertConfig.${secret}`)}
              </ActionButton>
            ) : null}
          </SettingRow>
        );
      })}
    </>
  );
}

// The rules and channels as the JSON the API takes them in; the spend detection is edited in its own rows and goes as
// it is held.
function readConfiguration(live: Config, values: Record<string, string>): ConfigInput | undefined {
  try {
    const rules = parseJSONLossless(values.rules ?? "");
    const channels = parseJSONLossless(values.channels ?? "");
    const lines = parseJSONLossless(values.lines ?? "");
    if (!Array.isArray(rules) || !Array.isArray(channels) || !Array.isArray(lines)) return undefined;
    // Keep parsed numbers exact. The API validates the fields and their bounds.
    return {
      rev: live.rev ?? 1,
      rules,
      channels,
      low_balance_thresholds: lines,
      ...(live.spend_detection ? { spend_detection: live.spend_detection } : {}),
    } as ConfigInput;
  } catch {
    return undefined;
  }
}

// AddChannel appends one channel to the configuration as it was read; the API checks the destination's form and host.
function AddChannel({ live, onClose }: { live: Config; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [view, reload] = useSnapshot(live);
  const [kind, setKind] = useState<Kind>("feishu");
  const channels = view.channels ?? [];
  return (
    <ChangeDialog
      operation="alert-channel-add"
      title={t("alertConfig.add")}
      action={t("alertConfig.add")}
      intro={<p>{t("alertConfig.addIntro")}</p>}
      fields={(error) => (
        <Stack>
          <Field label={t("alertConfig.channelId")} name="id" maxLength={64} error={errors.field(error, "channels")} />
          <Select
            label={t("alertConfig.channelKind")}
            name="kind"
            value={kind}
            onChange={(event) => setKind(event.target.value as Kind)}
            options={(Object.keys(kinds) as Kind[]).map((value) => ({ value, label: t(`alertConfig.kind.${value}`) }))}
          />
          <Field label={t(`alertConfig.destination.${kind}`)} name="destination" spellCheck={false} />
        </Stack>
      )}
      read={({ id = "", destination = "" }) => {
        const name = id.trim();
        if (!name || !destination.trim() || channels.some((c) => c.id === name)) {
          return { problem: t("alertConfig.invalidChannel") };
        }
        return {
          rev: view.rev ?? 1,
          rules: view.rules ?? [],
          channels: [...channels, { id: name, [kinds[kind].field]: destination.trim() }],
          ...(view.spend_detection ? { spend_detection: view.spend_detection } : {}),
        } as ConfigInput;
      }}
      send={putConfiguration}
      done={() => <p>{t("alertConfig.added")}</p>}
      reload={reload}
      onClose={onClose}
    />
  );
}

function Replace({ live, onClose }: { live: Config; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [view, reload] = useSnapshot(live);
  return (
    <ChangeDialog
      operation="alert-configuration"
      title={t("alertConfig.edit")}
      action={t("alertConfig.save")}
      intro={<p>{t("alertConfig.editIntro")}</p>}
      fields={(error) => (
        <Stack>
          <TextArea
            label={t("alertConfig.rules")}
            name="rules"
            rows={7}
            defaultValue={formatJSON(view.rules ?? [])}
            error={errors.field(error, "rules")}
            spellCheck={false}
          />
          <TextArea
            label={t("alertConfig.channels")}
            name="channels"
            rows={5}
            defaultValue={formatJSON(view.channels ?? [])}
            error={errors.field(error, "channels")}
            spellCheck={false}
          />
          <TextArea
            label={t("alertConfig.balanceLines")}
            name="lines"
            rows={4}
            defaultValue={formatJSON(view.low_balance_thresholds ?? [])}
            error={errors.field(error, "low_balance_thresholds")}
            spellCheck={false}
          />
        </Stack>
      )}
      read={(values) => readConfiguration(view, values) ?? { problem: t("alertConfig.invalid") }}
      send={putConfiguration}
      done={() => <p>{t("alertConfig.saved")}</p>}
      reload={reload}
      onClose={onClose}
    />
  );
}

function TestChannel({ id, onClose }: { id: string; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`test-alert-channel:${id}`}
      title={t("alertConfig.testChannel")}
      action={t("alertConfig.testChannel")}
      intro={<Code>{id}</Code>}
      read={() => id}
      send={(channel, { key, reason }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/alerts/tests", {
            params: { header: { "Idempotency-Key": key } },
            body: { channel, reason },
          }),
        )
      }
      done={(result) => (
        <p>
          {t("alertConfig.queued")} {result.id ? <Code>{result.id}</Code> : null}
        </p>
      )}
      onClose={onClose}
    />
  );
}

// Each kind's secret: its words, and the bounds the API holds it to (internal/observability channelKinds).
const secrets = {
  rotate: { label: "secret", intro: "rotateIntro", problem: "invalidSecret", min: 32, form: undefined },
  botSecret: { label: "secret", intro: "botSecretIntro", problem: "invalidBotSecret", min: 1, form: undefined },
  botToken: {
    label: "token",
    intro: "botSecretIntro",
    problem: "invalidToken",
    min: 1,
    form: /^\d{1,20}:[\w-]{20,100}$/,
  },
} as const;

// The secret is shown nowhere and replaced whole: a webhook's signing secret rotates, keeping the old one for an hour;
// a bot's signing secret or token replaces the old one at once.
function Rotate({ channel, live, onClose }: { channel: Channel; live: Config; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [view, reload] = useSnapshot(live);
  const rev = view.rev;
  const id = channel.id ?? "";
  const kind = kindOf(channel)?.[0];
  const which = kind ? kinds[kind].secret : undefined;
  if (rev === undefined || !which) return null;
  const rule = secrets[which];
  return (
    <ChangeDialog
      operation={`rotate-alert-channel-secret:${id}`}
      title={t(`alertConfig.${which}`)}
      action={t(`alertConfig.${which}`)}
      intro={
        <p>
          {t(`alertConfig.${rule.intro}`)} <Code>{id}</Code>
        </p>
      }
      diff={[{ label: id, before: t("drafts.secretSet"), after: t("drafts.secretNew") }]}
      warn={which === "rotate" ? t("alertConfig.rotateWarn") : undefined}
      fields={(error) => (
        <Field
          label={t(`alertConfig.${rule.label}`)}
          name="secret"
          type="password"
          autoComplete="new-password"
          error={errors.field(error, "secret")}
        />
      )}
      read={({ secret }) => {
        const size = new TextEncoder().encode(secret).length;
        const fits = secret && size >= rule.min && size <= 512 && (!rule.form || rule.form.test(secret));
        return fits ? secret : { problem: t(`alertConfig.${rule.problem}`) };
      }}
      send={(secret, { key, reason }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/alerts/channels/{channel_id}/secret", {
            params: { path: { channel_id: id }, header: { "Idempotency-Key": key } },
            body: { rev, secret, reason },
          }),
        )
      }
      done={() => <p>{t("alertConfig.rotated")}</p>}
      reload={reload}
      onClose={onClose}
    />
  );
}
