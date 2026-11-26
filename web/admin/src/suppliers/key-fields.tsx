import type { AdminSchemas } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Field, Stack, Tag, Text, TextArea } from "@axiom/ui";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { Problem } from "../change";
import { Code } from "../parts";
import { pastedKeys, serviceAccount } from "./upstream";

type T = TFunction<"admin">;
type Check = AdminSchemas["CredentialCheck"];

// The keys box (adA_upkNew's keys): one key a line, counted as they are pasted, with the repeats it will skip. Each key
// is a secret: the box is never filled back, and the browser does not keep or check it.
export function KeysField({ error }: { error?: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [typed, setTyped] = useState("");
  const { keys, duplicates } = pastedKeys(typed);
  const hint =
    keys.length === 0
      ? t("upstream.addDialog.keysHint")
      : duplicates > 0
        ? t("upstream.addDialog.keysCountedDup", { count: keys.length, dup: duplicates })
        : t("upstream.addDialog.keysCounted", { count: keys.length });
  return (
    <TextArea
      label={t("upstream.addDialog.keys")}
      hint={hint}
      name="keys"
      rows={5}
      value={typed}
      onChange={(event) => setTyped(event.target.value)}
      placeholder={t("upstream.addDialog.keysPlaceholder")}
      autoComplete="off"
      spellCheck={false}
      error={errors.field(error, "keys")}
    />
  );
}

// The service account box of Vertex AI: the project and the account are read out of the key as it is pasted, and the
// rest of it (the private key) is never shown back.
export function ServiceAccountField({ error }: { error?: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [typed, setTyped] = useState("");
  const read = serviceAccount(typed);
  return (
    <TextArea
      label={t("upstream.addDialog.json")}
      hint={read ? t("upstream.addDialog.jsonRead", read) : t("upstream.addDialog.jsonHint")}
      name="service_account_json"
      rows={5}
      value={typed}
      onChange={(event) => setTyped(event.target.value)}
      placeholder={'{ "type": "service_account", "project_id": … }'}
      autoComplete="off"
      spellCheck={false}
      error={errors.field(error, "credential/service_account_json")}
    />
  );
}

// The two ceilings every key or region has: calls at once, and calls a minute (none when left empty).
export function LimitFields({ error }: { error?: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <Field
        label={t("upstream.addDialog.concurrency")}
        hint={t("upstream.addDialog.concurrencyHint")}
        name="max_concurrency"
        inputMode="numeric"
        autoComplete="off"
        defaultValue="20"
        error={errors.field(error, "max_concurrency")}
      />
      <Field
        label={t("upstream.addDialog.rpm")}
        hint={t("upstream.addDialog.rpmHint")}
        name="requests_per_minute"
        inputMode="numeric"
        autoComplete="off"
        defaultValue="300"
        error={errors.field(error, "requests_per_minute")}
      />
    </>
  );
}

const whole = (raw: string, max: number) => /^\d+$/.test(raw) && Number(raw) >= 1 && Number(raw) <= max;

export function readLimits(
  values: Record<string, string>,
  t: T,
): { max_concurrency: number; requests_per_minute?: number } | Problem {
  const concurrency = (values.max_concurrency ?? "").trim();
  const rpm = (values.requests_per_minute ?? "").trim();
  if (!whole(concurrency, 2_147_483_647) || (rpm !== "" && !whole(rpm, 100_000))) {
    return { problem: t("upstream.addDialog.problems.number") };
  }
  return { max_concurrency: Number(concurrency), requests_per_minute: rpm === "" ? undefined : Number(rpm) };
}

// The pasted keys, each at least as long as a key the contract takes, at most the 100 one request carries.
export function readKeys(values: Record<string, string>, t: T): string[] | Problem {
  const { keys } = pastedKeys(values.keys ?? "");
  if (keys.length === 0) return { problem: t("upstream.addDialog.problems.keys") };
  if (keys.some((key) => key.length < 8)) return { problem: t("upstream.addDialog.problems.short") };
  if (keys.length > 100) return { problem: t("upstream.addDialog.problems.many") };
  return keys;
}

// The regions typed, one a line, in the platform's own spelling.
export function readRegions(values: Record<string, string>) {
  return [...new Set((values.regions ?? "").split(/[\s,，]+/).filter(Boolean))];
}

// An Azure resource's deployments, one a line: its name, and after a space the model it deploys when the name is not
// the model's; none when a line is not a deployment's name, or when there are none or more than a request takes.
export function readDeployments(values: Record<string, string>) {
  const lines = (values.deployments ?? "").split("\n").map((line) => line.trim().split(/\s+/).filter(Boolean));
  const named = lines.filter((words) => words.length > 0);
  const valid = named.every(
    ([name, model, ...rest]) =>
      /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(name ?? "") && rest.length === 0 && (model ?? "").length <= 200,
  );
  if (!valid || named.length === 0 || named.length > 100) return null;
  return named.map(([name = "", model]) => (model ? { name, model } : { name }));
}

export const validRegion = (region: string) => /^[a-z0-9][a-z0-9-]{0,39}$/.test(region);

// What each key or region came to (adres): its last four characters or its name, and whether it can be used.
export function Checks({ checks }: { checks: readonly Check[] }) {
  const { t } = useTranslation();
  return (
    <Stack gap={8}>
      {checks.map((check, at) => (
        <Text key={check.credential_id ?? `${check.index ?? at}-${check.region ?? ""}`} as="div">
          <Code>{check.region ?? (check.last4 ? `…${check.last4}` : String((check.index ?? at) + 1))}</Code>{" "}
          <Tag tone={check.outcome === "passed" ? "ok" : check.outcome === "failed" ? "bad" : "neutral"} dot>
            {check.cause
              ? `${t(`upstream.outcome.${check.outcome}`)}：${t(`upstream.cause.${check.cause}`)}`
              : t(`upstream.outcome.${check.outcome}`)}
          </Tag>
        </Text>
      ))}
    </Stack>
  );
}
