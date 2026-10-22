import type { AdminSchemas } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Button, Icon, TextField } from "@axiom/ui";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "./change";
import { Facts, State } from "./parts";

type Limits = AdminSchemas["KeyLimits"];
type View = AdminSchemas["KeyLimitsView"];

// The limits on the call keys of an account, each with the bounds the contract states. A limit left empty is not set,
// and the level above applies: the default in code for a site, the site's for an account.
const limits = [{ name: "max_keys" }, { name: "creations_per_hour" }] as const;
const min = 1;
const max = 10_000;

// The limits as typed: a limit left empty is left out, and one that is not a whole number in the bounds is a problem.
export function readKeyLimits(values: Record<string, string>, problem: string): Limits | Problem {
  const read: Limits = {};
  for (const { name } of limits) {
    const typed = (values[name] ?? "").trim();
    if (typed === "") {
      continue;
    }
    const count = Number(typed);
    if (!/^\d+$/.test(typed) || count < min || count > max) {
      return { problem };
    }
    read[name] = count;
  }
  return read;
}

type Send = (limits: Limits, context: { reason: string; key: string }) => Promise<{ data: View; replayed: boolean }>;

// KeyLimitsCard shows what an account of a site, or the site's accounts, are held to, and where it comes from; with
// the right to, it opens the change.
export function KeyLimitsCard({
  level,
  view,
  send,
  editable,
}: {
  level: "site" | "account";
  view: View;
  send: Send;
  editable: boolean;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const rows = limits.map(({ name }): [string, ReactNode] => [
    t(`settings.keyLimits.field.${name}`),
    <span key={name}>
      {view.effective[name]}{" "}
      <State tone="neutral">
        {view.limits[name] === undefined ? t(`settings.keyLimits.from.${level}`) : t("settings.keyLimits.own")}
      </State>
    </span>,
  ]);
  return (
    <>
      <Facts
        title={t(`settings.keyLimits.title.${level}`)}
        rows={rows}
        action={
          editable ? (
            <Button size="md" onClick={() => setOpen(true)}>
              <Icon name="pencil" />
              {t("settings.keyLimits.edit")}
            </Button>
          ) : undefined
        }
      />
      {open ? <KeyLimitsDialog level={level} view={view} send={send} onClose={() => setOpen(false)} /> : null}
    </>
  );
}

function KeyLimitsDialog({
  level,
  view,
  send,
  onClose,
}: {
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
      title={t("settings.keyLimits.edit")}
      intro={<p>{t(`settings.keyLimits.intro.${level}`)}</p>}
      action={t("settings.keyLimits.save")}
      fields={(error) => (
        <>
          {limits.map(({ name }) => (
            <TextField
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
      read={(values) => readKeyLimits(values, problem)}
      send={send}
      done={() => <p>{t("settings.keyLimits.saved")}</p>}
      onClose={onClose}
    />
  );
}
