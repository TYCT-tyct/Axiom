import type { AdminSchemas } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Field } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "./change";

type Limits = AdminSchemas["KeyLimits"];
type View = AdminSchemas["KeyLimitsView"];

// The limits on the call keys of an account, each with the bounds the contract states. A limit left empty is not set,
// and the level above applies: the default in code for a site, the site's for an account.
const min = 1;
const max = 10_000;
const limits = [
  { name: "max_keys", min, max },
  { name: "creations_per_hour", min, max },
] as const;

// readBoundedInts parses optional whole-number limits against their contract bounds; an empty field is omitted.
export function readBoundedInts<K extends string>(
  values: Record<string, string>,
  specs: readonly { name: K; min: number; max: number }[],
  problem: string,
): Partial<Record<K, number>> | Problem {
  const read: Partial<Record<K, number>> = {};
  for (const spec of specs) {
    const typed = (values[spec.name] ?? "").trim();
    if (typed === "") {
      continue;
    }
    const count = Number(typed);
    if (!/^\d+$/.test(typed) || count < spec.min || count > spec.max) {
      return { problem };
    }
    read[spec.name] = count;
  }
  return read;
}

type Send = (limits: Limits, context: { reason: string; key: string }) => Promise<{ data: View; replayed: boolean }>;

export function KeyLimitsDialog({
  operation,
  level,
  view,
  send,
  onClose,
}: {
  operation: string;
  level: "site" | "account";
  view: View;
  send: Send;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const problem = t("settings.keyLimits.invalid", { min, max });
  return (
    <ChangeDialog
      operation={operation}
      title={t("settings.keyLimits.edit")}
      intro={<p>{t(`settings.keyLimits.intro.${level}`)}</p>}
      action={t("settings.keyLimits.save")}
      fields={(error) => (
        <>
          {limits.map(({ name }) => (
            <Field
              key={name}
              label={t(`settings.keyLimits.field.${name}`)}
              hint={t(`settings.keyLimits.hint.${level}`, { min, max })}
              name={name}
              inputMode="numeric"
              autoComplete="off"
              defaultValue={view.limits[name]}
              error={errors.field(error, `limits/${name}`)}
            />
          ))}
        </>
      )}
      read={(values) => readBoundedInts(values, limits, problem)}
      send={send}
      done={() => <p>{t("settings.keyLimits.saved")}</p>}
      onClose={onClose}
    />
  );
}
