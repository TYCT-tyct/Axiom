import { ApiError } from "@axiom/api";
import {
  Card,
  ChipField,
  DataState,
  NumberField,
  SecretField,
  SegmentedControl,
  SelectBox,
  Switch,
  TextBox,
} from "@axiom/ui";
import type { UseQueryResult } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { DraftRow, type SettingDrafts } from "./settings-drafts";
import type { SettingKey } from "./settings-registry";

// The rows a settings page is made of, each one a registered setting's control in its DraftRow: the page says which
// setting and what its control needs (a unit, a bound), and the control reads and writes the setting's draft.

// SettingsCard is a card of rows with where its changes reach in the top right (adCard in admin_set.js).
export function SettingsCard({
  scope,
  title,
  lede,
  actions,
  children,
}: {
  scope: "site" | "platform";
  title: string;
  lede?: string;
  actions?: ReactNode;
  children: ReactNode;
}) {
  const { t } = useTranslation();
  return (
    <Card
      layout="rows"
      title={title}
      lede={lede}
      actions={actions}
      scope={{ label: t(`scope.${scope}`), tip: t(`scope.${scope}Tip`) }}
    >
      {children}
    </Card>
  );
}

// OptionalView is a view a node of the release before may not have: its 404 is the page working without that section,
// which the page then leaves out, and anything else is the view's ordinary states.
export function OptionalView<T>({
  query,
  children,
}: {
  query: UseQueryResult<T>;
  children: (view: T | undefined) => ReactNode;
}) {
  if (query.error instanceof ApiError && query.error.status === 404) return children(undefined);
  return <DataState query={query}>{(view) => children(view)}</DataState>;
}

type Props<K extends SettingKey> = { drafts: SettingDrafts<K>; setting: K; disabled?: boolean };

export function NumberSetting<K extends SettingKey>({
  drafts,
  setting,
  unit,
  prefix,
  decimals,
  min,
  max,
  disabled,
}: Props<K> & { unit?: string; prefix?: string; decimals?: number; min?: number; max?: number }) {
  return (
    <DraftRow drafts={drafts} setting={setting}>
      <NumberField
        label={drafts.label(setting)}
        value={drafts.value(setting) as string}
        onChange={(value) => drafts.set(setting, value)}
        unit={unit}
        prefix={prefix}
        decimals={decimals}
        min={min}
        max={max}
        invalid={drafts.invalid(setting)}
        disabled={disabled}
      />
    </DraftRow>
  );
}

export function SwitchSetting<K extends SettingKey>({ drafts, setting, disabled }: Props<K>) {
  return (
    <DraftRow drafts={drafts} setting={setting}>
      <Switch
        label={drafts.label(setting)}
        checked={drafts.value(setting) === true}
        disabled={disabled}
        onChange={(on) => drafts.set(setting, on)}
      />
    </DraftRow>
  );
}

export function TextSetting<K extends SettingKey>({
  drafts,
  setting,
  width,
  mono,
  swatch,
  placeholder,
  maxLength,
  disabled,
}: Props<K> & { width?: number; mono?: boolean; swatch?: boolean; placeholder?: string; maxLength?: number }) {
  const value = drafts.value(setting) as string;
  return (
    <DraftRow drafts={drafts} setting={setting}>
      <TextBox
        label={drafts.label(setting)}
        value={value}
        onChange={(next) => drafts.set(setting, next)}
        width={width}
        mono={mono}
        swatch={swatch ? value : undefined}
        placeholder={placeholder}
        maxLength={maxLength}
        invalid={drafts.invalid(setting)}
        disabled={disabled}
      />
    </DraftRow>
  );
}

// A secret is write-only: its value is the new one typed, empty while the saved one is left, and the row says only
// whether one is saved.
export function SecretSetting<K extends SettingKey>({
  drafts,
  setting,
  isSet,
  disabled,
}: Props<K> & { isSet: boolean }) {
  const { t } = useTranslation();
  return (
    <DraftRow drafts={drafts} setting={setting}>
      <SecretField
        label={drafts.label(setting)}
        isSet={isSet}
        value={drafts.value(setting) as string}
        onChange={(next) => drafts.set(setting, next)}
        setLabel={t("drafts.secretSet")}
        unsetLabel={t("drafts.secretUnset")}
        replaceLabel={t("drafts.secretReplace")}
        placeholder={t("drafts.secretNew")}
        disabled={disabled}
      />
    </DraftRow>
  );
}

export function ChoiceSetting<K extends SettingKey, V extends string>({
  drafts,
  setting,
  options,
}: Props<K> & { options: readonly { value: V; label: string }[] }) {
  // A segmented control holds three options on a phone; more are a select.
  const Control = options.length > 3 ? SelectBox : SegmentedControl;
  return (
    <DraftRow drafts={drafts} setting={setting}>
      <Control
        label={drafts.label(setting)}
        value={drafts.value(setting) as V}
        options={options}
        onChange={(value) => drafts.set(setting, value)}
      />
    </DraftRow>
  );
}

export function ChipSetting<K extends SettingKey>({
  drafts,
  setting,
  placeholder,
}: Props<K> & { placeholder?: string }) {
  const { t } = useTranslation();
  return (
    <DraftRow drafts={drafts} setting={setting}>
      <ChipField
        label={drafts.label(setting)}
        values={drafts.value(setting) as readonly string[]}
        onChange={(values) => drafts.set(setting, values)}
        removeLabel={(value) => t("drafts.remove", { value })}
        placeholder={placeholder}
        mono
      />
    </DraftRow>
  );
}
