// What one take returned: the codes the server cleared, or why it could not answer.
export type PendingCodesResult = { codes: string[] } | { failure: unknown };

let inFlight: { binding: string; attempt: number; result: Promise<PendingCodesResult> } | null = null;

// takePending is the one take for a session and attempt. The server clears the codes when it answers, so a second
// request -- StrictMode's second effect, a remount, or a page opened after the answer -- would only ever see nothing.
// Reusing the promise keeps the only copy and lets a later caller show it, even when the caller that started the
// request is gone.
export function takePending(
  binding: string,
  attempt: number,
  send: () => Promise<{ recovery_codes: string[] }>,
): Promise<PendingCodesResult> {
  if (inFlight?.binding === binding && inFlight.attempt === attempt) {
    return inFlight.result;
  }
  const result = send().then(
    ({ recovery_codes }) => ({ codes: recovery_codes }),
    (failure: unknown) => ({ failure }),
  );
  inFlight = { binding, attempt, result };
  return result;
}

// forgetPending drops the result of a session that is over, so its codes do not stay in memory.
export function forgetPending(binding: string) {
  if (inFlight?.binding === binding) {
    inFlight = null;
  }
}
