import { consoleApi, unwrap } from "@axiom/api";
import {
  accountKey,
  claimSession,
  consumeEmailChangeToken,
  endUnauthenticated,
  holdsIdentity,
  identityOf,
  type SessionClaim,
  stripEmailChangeToken,
  useCachedSession,
  useShowRecoveryCodes,
  useStartSession,
} from "@axiom/app";
import { type QueryClient, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";

export type EmailConfirmStatus =
  | { state: "idle" }
  | { state: "pending" }
  | { state: "confirmed" }
  | { state: "error"; error: unknown };

type ConfirmTask = {
  token: string;
  identity: string;
  status: EmailConfirmStatus;
  delivered: boolean;
  listeners: Set<(status: EmailConfirmStatus) => void>;
};

const confirmTasks = new Map<string, ConfirmTask>();

type ConfirmDeps = {
  client: QueryClient;
  start: (csrfToken: string) => Promise<unknown>;
  showCodes: (change: { csrf_token: string; recovery_codes: string[] }, identity: string) => void;
};

// Reuse the active request per (identity, token) across StrictMode remounts without cancelling in-flight POSTs, and
// evict finished or failed entries so reopening the same link after a network outage starts a fresh attempt.
function runEmailConfirmTask(claim: SessionClaim, token: string, deps: ConfirmDeps): ConfirmTask {
  const existing = confirmTasks.get(claim.identity);
  if (existing && existing.token === token && existing.status.state === "pending") return existing;
  const task: ConfirmTask = {
    token,
    identity: claim.identity,
    status: { state: "pending" },
    delivered: false,
    listeners: new Set(),
  };
  confirmTasks.set(claim.identity, task);
  const finish = (nextStatus: EmailConfirmStatus) => {
    task.status = nextStatus;
    for (const listener of task.listeners) listener(nextStatus);
    if (task.delivered && confirmTasks.get(claim.identity) === task) confirmTasks.delete(claim.identity);
  };
  void unwrap(consoleApi.POST("/api/console/v1/profile/email/confirm", { body: { token } }))
    .then(async (value) => {
      if (!holdsIdentity(deps.client, claim.identity)) {
        if (confirmTasks.get(claim.identity) === task) confirmTasks.delete(claim.identity);
        return;
      }
      if (value.recovery_codes?.length && value.csrf_token) {
        deps.showCodes({ csrf_token: value.csrf_token, recovery_codes: value.recovery_codes }, claim.identity);
      }
      await deps.start(value.csrf_token ?? "").catch(() => {});
      if (holdsIdentity(deps.client, claim.identity)) {
        await deps.client.invalidateQueries({ queryKey: [accountKey] });
      }
      finish({ state: "confirmed" });
    })
    .catch((error: unknown) => {
      endUnauthenticated(deps.client, error, claim.binding);
      if (!holdsIdentity(deps.client, claim.identity)) {
        if (confirmTasks.get(claim.identity) === task) confirmTasks.delete(claim.identity);
        return;
      }
      finish({ state: "error", error });
    });
  return task;
}

function attachConfirmListener(task: ConfirmTask, onUpdate: (next: EmailConfirmStatus) => void): () => void {
  let active = true;
  const deliver = (next: EmailConfirmStatus) => {
    if (!active) return;
    onUpdate(next);
    if (next.state !== "pending") {
      task.delivered = true;
      if (confirmTasks.get(task.identity) === task) confirmTasks.delete(task.identity);
    }
  };
  task.listeners.add(deliver);
  deliver(task.status);
  return () => {
    active = false;
    task.listeners.delete(deliver);
  };
}

function resolveConfirmTask(identity: string, deps: ConfirmDeps): ConfirmTask | null {
  if (!identity) return null;
  const claim = claimSession(deps.client);
  if (!claim) return null;
  const loc = window.location;
  const { token: hashToken, cleanHref } = stripEmailChangeToken(`${loc.pathname}${loc.search}${loc.hash}`);
  const stashedToken = consumeEmailChangeToken();
  const token = hashToken ?? stashedToken;
  if (token) {
    if (hashToken) window.history.replaceState(null, "", cleanHref);
    return runEmailConfirmTask(claim, token, deps);
  }
  const pending = confirmTasks.get(claim.identity);
  return pending && !pending.delivered ? pending : null;
}

export function useEmailConfirm(): { status: EmailConfirmStatus; dismiss: () => void } {
  const start = useStartSession();
  const showCodes = useShowRecoveryCodes();
  const client = useQueryClient();
  const identity = identityOf(useCachedSession());
  const depsRef = useRef({ start, showCodes, client });
  depsRef.current = { start, showCodes, client };
  const [status, setStatus] = useState<EmailConfirmStatus>({ state: "idle" });

  useEffect(() => {
    let detach: (() => void) | undefined;
    const sync = () => {
      const task = resolveConfirmTask(identity, depsRef.current);
      if (!task) return;
      detach?.();
      detach = attachConfirmListener(task, setStatus);
    };
    sync();
    window.addEventListener("hashchange", sync);
    return () => {
      detach?.();
      window.removeEventListener("hashchange", sync);
    };
  }, [identity]);

  return { status, dismiss: () => setStatus({ state: "idle" }) };
}
