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
  SettingRow,
  Stack,
  Tag,
  TextArea,
  useFormat,
} from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
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
type Edit = { kind: "configuration" } | { kind: "test" | "secret"; id: string };

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
      <>
        <PageHead title={t("shell.nav.alertRules")} lede={t("alertConfig.description")} />
        <PageBody>
          <DataState
            query={query}
            isEmpty={(view) => view.rev === undefined}
            empty={<EmptyState title={t("alertConfig.unavailable")} />}
          >
            {() => null}
          </DataState>
        </PageBody>
      </>
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
        return unwrapChange(
          adminApi.PUT("/api/admin/v1/alerts/configuration", {
            params: { header: { "Idempotency-Key": key } },
            body: { ...body, reason },
            bodySerializer: formatJSON,
          }),
        );
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
      <PageHead
        title={t("shell.nav.alertRules")}
        lede={t("alertConfig.description")}
        actions={
          <ActionButton asChild>
            <Link to="/alert-events">{t("alertConfig.seeEvents")}</Link>
          </ActionButton>
        }
      />
      <PageBody>
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
        <SettingsCard scope="platform" title={t("alertConfig.channels")}>
          <Channels channels={view.channels ?? []} edit={setEdit} />
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
      {edit?.kind === "secret" ? <Rotate id={edit.id} live={view} onClose={close} /> : null}
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

function Channels({ channels, edit }: { channels: Channel[]; edit: (value: Edit) => void }) {
  const { t } = useTranslation();
  if (!channels.length) return <EmptyState compact title={t("alertConfig.noChannels")} />;
  return (
    <>
      {channels.map((channel) =>
        channel.id ? (
          <SettingRow
            key={channel.id}
            title={channel.id}
            detail={<Code>{channel.email ?? channel.webhook_url ?? "—"}</Code>}
          >
            <ActionButton size="sm" onClick={() => edit({ kind: "test", id: channel.id ?? "" })}>
              {t("alertConfig.testChannel")}
            </ActionButton>
            {channel.webhook_url ? (
              <ActionButton size="sm" onClick={() => edit({ kind: "secret", id: channel.id ?? "" })}>
                {t("alertConfig.rotate")}
              </ActionButton>
            ) : null}
          </SettingRow>
        ) : null,
      )}
    </>
  );
}

// The rules and channels as the JSON the API takes them in; the spend detection is edited in its own rows and goes as
// it is held.
function readConfiguration(live: Config, values: Record<string, string>): ConfigInput | undefined {
  try {
    const rules = parseJSONLossless(values.rules ?? "");
    const channels = parseJSONLossless(values.channels ?? "");
    if (!Array.isArray(rules) || !Array.isArray(channels)) return undefined;
    // Keep parsed numbers exact. The API validates the fields and their bounds.
    return {
      rev: live.rev ?? 1,
      rules,
      channels,
      ...(live.spend_detection ? { spend_detection: live.spend_detection } : {}),
    } as ConfigInput;
  } catch {
    return undefined;
  }
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
        </Stack>
      )}
      read={(values) => readConfiguration(view, values) ?? { problem: t("alertConfig.invalid") }}
      send={(values, { key, reason }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/alerts/configuration", {
            params: { header: { "Idempotency-Key": key } },
            body: { ...values, reason },
            bodySerializer: formatJSON,
          }),
        )
      }
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

// The secret is shown nowhere and replaced whole: a webhook's signing secret rotates, keeping the old one for an hour.
function Rotate({ id, live, onClose }: { id: string; live: Config; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [view, reload] = useSnapshot(live);
  const rev = view.rev;
  if (rev === undefined) return null;
  return (
    <ChangeDialog
      operation={`rotate-alert-channel-secret:${id}`}
      title={t("alertConfig.rotate")}
      action={t("alertConfig.rotate")}
      intro={
        <p>
          {t("alertConfig.rotateIntro")} <Code>{id}</Code>
        </p>
      }
      diff={[{ label: id, before: t("drafts.secretSet"), after: t("drafts.secretNew") }]}
      warn={t("alertConfig.rotateWarn")}
      fields={(error) => (
        <Field
          label={t("alertConfig.secret")}
          name="secret"
          type="password"
          autoComplete="new-password"
          error={errors.field(error, "secret")}
        />
      )}
      read={({ secret }) => {
        const size = new TextEncoder().encode(secret).length;
        return secret && size >= 32 && size <= 512 ? secret : { problem: t("alertConfig.invalidSecret") };
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
