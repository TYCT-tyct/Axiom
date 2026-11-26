import type { AdminOperation } from "@axiom/api";
import { accountKey, useErrors } from "@axiom/app";
import { Dialog } from "@axiom/boards/ds";
import { SettingRow, Stack, Text, UnsavedBar } from "@axiom/ui";
import { useQueryClient } from "@tanstack/react-query";
import { useBlocker, useSearch } from "@tanstack/react-router";
import { type ReactNode, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Context, type Problem } from "./change";
import {
  type OperationOf,
  type SettingEntry,
  type SettingKey,
  settingKeys,
  settingsRegistry,
} from "./settings-registry";
import { say } from "./toasts";

// A settings page edits its rows in place and submits them together (admin_set.js): a row that differs from what is
// saved is a draft, marked and revertible on its own; the bar at the bottom counts them; one dialog shows every draft
// before and after and takes one reason; and the drafts go out one operation at a time, in the registry's order, each
// under its own Idempotency-Key. The invariants:
// - a draft is sent only against the saved value it was made from: one whose saved value moved meanwhile is held back,
//   so nobody's change is overwritten, as a 409 stale_revision holds back a whole operation;
// - an operation that succeeded clears its drafts; the first that fails stops the submit and keeps its drafts and
//   those after it, with its error on its rows;
// - a submit whose answer was lost is the same requests with the same keys when sent again (ChangeDialog), so the
//   operations already done are not done twice.

// A setting's value in the form its control holds it: a number is its typed text, a list of domains its entries.
export type SettingValue = string | boolean | readonly string[];
type Values<K extends SettingKey> = Record<K, SettingValue>;
type Sent = { data: unknown; replayed: boolean };
// A write is the page's request for one operation, from the values of all its rows, its drafts in place.
type SettingWrite<K extends SettingKey> = (values: Values<K>, context: Context) => Promise<Sent>;

type Options<K extends SettingKey> = {
  // What the submit changes, the same each time (ChangeDialog's operation): "settings:registration:" + the site.
  scope: string;
  // What is saved now, for every row of the page.
  saved: Values<K>;
  writes: { [O in OperationOf<K>]: SettingWrite<K> };
  // Whether a value can be sent; a page whose rows all can leaves it out.
  check?: (key: K, value: SettingValue) => boolean;
  // How a value reads in the before and after, where the plain words of a value would not do (a mode's name).
  show?: (key: K, value: SettingValue) => string | undefined;
  // What is wrong across rows, which no row's own check can see (a minimum above its maximum), with every row's value
  // as the submit would send it; undefined when nothing is. The dialog says it and sends nothing.
  verify?: (values: Values<K>) => string | undefined;
  // One line on what could go wrong with the drafts as they stand, shown in the submit dialog (adA_submit's warn).
  warn?: (values: Values<K>, changed: readonly K[]) => string | undefined;
};

type Draft = { value: SettingValue; base: SettingValue };
type Step<K extends SettingKey> = {
  operation: AdminOperation;
  keys: K[];
  values: Values<K>;
  before: Values<K>;
  key: string;
  write: SettingWrite<K>;
};
// A submit as it went out: its steps' bodies and keys are fixed in it, and done says which have been answered, so
// sending it again sends only the rest, exactly as they were.
type Plan<K extends SettingKey> = { steps: Step<K>[]; done: Set<AdminOperation> };
type Outcome = { ok: true } | { ok: false; error: unknown };

const same = (a: SettingValue | undefined, b: SettingValue | undefined) => JSON.stringify(a) === JSON.stringify(b);
const operationOf = (key: SettingKey): AdminOperation => settingsRegistry[key].operation;
type Writes<K extends SettingKey> = Record<AdminOperation, SettingWrite<K>>;

// useDrafts holds a page's drafts against what is saved: a draft equal to the saved value is no draft.
function useDrafts<K extends SettingKey>(saved: Values<K>) {
  const [drafts, setDrafts] = useState<Partial<Record<K, Draft>>>({});
  const draftOf = (key: K) => drafts[key];
  const dirty = (key: K) => {
    const draft = draftOf(key);
    return draft !== undefined && !same(draft.value, saved[key]);
  };
  const set = (key: K, next: SettingValue) =>
    setDrafts((now) => {
      const { [key]: _, ...rest } = now;
      if (same(next, saved[key])) return rest as Partial<Record<K, Draft>>;
      return { ...now, [key]: { value: next, base: now[key]?.base ?? saved[key] } };
    });
  // drop takes out the drafts of keys for which keep says no.
  const drop = (keep: (key: K, draft: Draft | undefined) => boolean) =>
    setDrafts((now) => {
      const left = { ...now };
      for (const key of Object.keys(now) as K[]) if (!keep(key, now[key])) delete left[key];
      return left;
    });
  return {
    draftOf,
    dirty,
    set,
    drop,
    discard: () => setDrafts({}),
    value: (key: K) => (dirty(key) ? (draftOf(key) as Draft).value : saved[key]),
  };
}

// planOf is the submit of the drafts as it will go out: one step per operation, in the registry's order, each with the
// whole of its values and a key of its own.
function planOf<K extends SettingKey>(
  changed: K[],
  saved: Values<K>,
  value: (key: K) => SettingValue,
  writes: Writes<K>,
): Plan<K> {
  const operations = [...new Set(changed.map(operationOf))];
  const steps = operations.map((operation): Step<K> => {
    const own = changed.filter((key) => operationOf(key) === operation);
    const values = { ...saved };
    for (const key of own) values[key] = value(key);
    return { operation, keys: own, values, before: { ...saved }, key: crypto.randomUUID(), write: writes[operation] };
  });
  return { steps, done: new Set() };
}

// runPlan sends the steps not yet answered, in order, and stops at the first that fails.
async function runPlan<K extends SettingKey>(
  submit: Plan<K>,
  reason: string,
  told: (step: Step<K>, outcome: Outcome) => Promise<void> | void,
) {
  let replayed = true;
  for (const step of submit.steps) {
    if (submit.done.has(step.operation)) continue;
    let sent: Sent;
    try {
      sent = await step.write(step.values, { reason, key: step.key });
    } catch (error) {
      await told(step, { ok: false, error });
      throw error;
    }
    submit.done.add(step.operation);
    replayed &&= sent.replayed;
    await told(step, { ok: true });
  }
  return { data: submit, replayed };
}

// undoPlan is the inverse of a submit: every step done puts back the values from before it, the last first, each under
// a key of its own, with the page's write and saved values as they are now.
async function undoPlan<K extends SettingKey>(
  done: Plan<K>,
  { reason, key }: Context,
  now: Pick<Options<K>, "saved" | "writes">,
) {
  const steps = done.steps.filter((step) => done.done.has(step.operation)).reverse();
  for (const [at, step] of steps.entries()) {
    const values = { ...now.saved };
    for (const own of step.keys) values[own] = step.before[own];
    await (now.writes as Writes<K>)[step.operation](values, { reason, key: at === 0 ? key : crypto.randomUUID() });
  }
}

export function useSettingDrafts<K extends SettingKey>(options: Options<K>) {
  const { saved, check } = options;
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();
  const drafts = useDrafts(saved);
  const { dirty, value } = drafts;
  const [outcomes, setOutcomes] = useState<Partial<Record<AdminOperation, Outcome>>>({});
  // The row the palette led here to (settingSearchItems), named in the page's search.
  const flash = (useSearch({ strict: false }) as { setting?: unknown }).setting;
  // An undo is sent after its dialog closed, against what the page has then.
  const live = useRef(options);
  live.current = options;
  const changed = settingKeys.filter((key): key is K => key in saved && dirty(key as K));
  const label = (key: K) => t(settingsRegistry[key].label);
  const plain = { on: t("drafts.on"), off: t("drafts.off"), none: t("drafts.none") };
  const names = (of: K[]) => new Intl.ListFormat(i18n.language).format(of.map(label));
  const failed = (key: K) => outcomes[operationOf(key)]?.ok === false;

  // Every row's value as a submit would send it: the drafts in place of what is saved.
  const now = () => Object.fromEntries(Object.keys(saved).map((key) => [key, value(key as K)])) as Values<K>;
  const plan = (): Plan<K> | Problem => {
    const moved = changed.filter((key) => !same(drafts.draftOf(key)?.base, saved[key]));
    if (moved.length) return { problem: t("drafts.moved", { names: names(moved) }) };
    const across = options.verify?.(now());
    if (across) return { problem: across };
    return planOf(changed, saved, value, options.writes as Writes<K>);
  };
  const told = async (step: Step<K>, outcome: Outcome) => {
    setOutcomes((now) => ({ ...now, [step.operation]: outcome }));
    if (!outcome.ok) return;
    // The row shows the saved value before its draft goes, so it never flashes back to the old one; a row typed again
    // while its step was on its way keeps what was typed.
    await queryClient.invalidateQueries({ queryKey: [accountKey] });
    drafts.drop((key, draft) => !step.keys.includes(key) || !same(draft?.value, step.values[key]));
  };

  return {
    changed,
    value,
    set: drafts.set,
    dirty,
    revert: (key: K) => drafts.set(key, saved[key]),
    invalid: (key: K) => dirty(key) && check !== undefined && !check(key, value(key)),
    failure: (key: K) => {
      const outcome = outcomes[operationOf(key)];
      return outcome?.ok === false && dirty(key) ? outcome.error : undefined;
    },
    flash: (key: K) => flash === key,
    discard: drafts.discard,
    // Drafts of the operation a stale revision refused are given up for what is current (the dialog's reload).
    reload: () => drafts.drop((key) => !failed(key)),
    diff: () =>
      changed.map((key) => ({
        label: label(key),
        before: shown(options, plain, key, saved[key]),
        after: shown(options, plain, key, value(key)),
      })),
    label,
    names,
    outcomes,
    resetOutcomes: () => setOutcomes({}),
    plan,
    run: (submit: Plan<K>, { reason }: Context) => runPlan(submit, reason, told),
    undo: (done: Plan<K>, context: Context) => undoPlan(done, context, live.current),
    warning: () => options.warn?.(now(), changed),
    scope: options.scope,
  };
}

export type SettingDrafts<K extends SettingKey> = ReturnType<typeof useSettingDrafts<K>>;

type Plain = { on: string; off: string; none: string };

function shown<K extends SettingKey>({ show }: Options<K>, plain: Plain, key: K, value: SettingValue): string {
  const own = show?.(key, value);
  if (own !== undefined) return own;
  if (typeof value === "boolean") return value ? plain.on : plain.off;
  if (typeof value === "string") return value;
  return value.length ? value.join(", ") : plain.none;
}

// SettingsSubmit is a settings page's bar and what it opens: the dialog that submits every draft, and the question
// asked before the page is left with drafts. ⌘S opens the dialog as the bar's button does.
export function SettingsSubmit<K extends SettingKey>({ drafts }: { drafts: SettingDrafts<K> }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const count = drafts.changed.length;
  const wrong = drafts.changed.filter(drafts.invalid).length;
  return (
    <>
      <UnsavedBar
        open={count > 0}
        message={wrong ? t("drafts.invalid", { count: wrong }) : t("drafts.pending", { count })}
        discard={{
          label: t("drafts.discard"),
          onClick: () => {
            drafts.discard();
            say(t("drafts.discarded"));
          },
        }}
        submit={{
          label: t("drafts.review"),
          onClick: () => {
            drafts.resetOutcomes();
            setOpen(true);
          },
        }}
        submitDisabled={wrong > 0}
        shortcut="⌘S"
      />
      {open ? <SubmitDialog drafts={drafts} onClose={() => setOpen(false)} /> : null}
      <LeaveGuard dirty={count > 0} />
    </>
  );
}

function SubmitDialog<K extends SettingKey>({ drafts, onClose }: { drafts: SettingDrafts<K>; onClose: () => void }) {
  const { t } = useTranslation();
  // The count the dialog was opened with: rows clear as their operations go through.
  const [count] = useState(drafts.changed.length);
  return (
    <ChangeDialog
      operation={drafts.scope}
      title={t("drafts.title", { count })}
      intro={<p>{t("drafts.intro")}</p>}
      diff={drafts.diff()}
      warn={drafts.warning()}
      action={t("drafts.action")}
      fields={() => <Outcomes drafts={drafts} />}
      read={drafts.plan}
      send={drafts.run}
      done={() => <p>{t("drafts.done")}</p>}
      toast={() => t("drafts.submitted", { count })}
      undo={drafts.undo}
      reload={drafts.reload}
      onClose={onClose}
    />
  );
}

// Outcomes says, once a submit has gone out, which of its operations went through, which failed and which were not
// sent; nothing before.
function Outcomes<K extends SettingKey>({ drafts }: { drafts: SettingDrafts<K> }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const tried = Object.entries(drafts.outcomes);
  if (!tried.length) return null;
  const went = settingKeys.filter((key) => drafts.outcomes[settingsRegistry[key].operation]?.ok) as K[];
  const lines: ReactNode[] = [];
  if (went.length)
    lines.push(
      <Text key="went" tone="ok">
        {t("drafts.went", { names: drafts.names(went) })}
      </Text>,
    );
  for (const [operation, outcome] of tried) {
    if (!outcome || outcome.ok) continue;
    const own = drafts.changed.filter((key) => settingsRegistry[key].operation === operation);
    lines.push(
      <Text key={operation} tone="bad">
        {t("drafts.failed", { names: drafts.names(own), message: errors.message(outcome.error) })}
      </Text>,
    );
  }
  const waiting = drafts.changed.filter((key) => !drafts.outcomes[settingsRegistry[key].operation]);
  if (waiting.length) {
    lines.push(
      <Text key="waiting" tone="muted">
        {t("drafts.waiting", { names: drafts.names(waiting) })}
      </Text>,
    );
  }
  return <Stack gap={8}>{lines}</Stack>;
}

// LeaveGuard asks before the page is left while it holds drafts: by a link of the admin, through the router, or by
// closing or reloading the tab, through the browser's own question. A change of the page's own search is not leaving.
function LeaveGuard({ dirty }: { dirty: boolean }) {
  const { t } = useTranslation();
  const blocker = useBlocker({
    shouldBlockFn: ({ current, next }) => dirty && current.pathname !== next.pathname,
    enableBeforeUnload: () => dirty,
    withResolver: true,
  });
  if (blocker.status !== "blocked") return null;
  return (
    <Dialog
      title={t("drafts.leaveTitle")}
      closeLabel={t("dialog.close", { ns: "common" })}
      onClose={blocker.reset}
      primary={{ label: t("drafts.leave"), danger: true, onClick: blocker.proceed }}
      secondary={{ label: t("drafts.stay"), onClick: blocker.reset }}
    >
      <p>{t("drafts.leaveBody")}</p>
    </Dialog>
  );
}

// DraftRow is a registered setting's row on its page: its words from the registry, the mark and revert of a draft, the
// error of its operation's last failed submit, and the flash when the palette led here. children is its control.
export function DraftRow<K extends SettingKey>({
  drafts,
  setting,
  children,
}: {
  drafts: SettingDrafts<K>;
  setting: K;
  children: ReactNode;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const { detail } = settingsRegistry[setting] as SettingEntry;
  const error = drafts.failure(setting);
  const name = drafts.label(setting);
  return (
    <SettingRow
      title={name}
      detail={error === undefined ? detail && t(detail) : <Text tone="bad">{errors.message(error)}</Text>}
      dirty={drafts.dirty(setting)}
      reset={{
        label: t("drafts.revert"),
        name: t("drafts.revertName", { name }),
        onReset: () => drafts.revert(setting),
      }}
      flash={drafts.flash(setting)}
    >
      {children}
    </SettingRow>
  );
}
