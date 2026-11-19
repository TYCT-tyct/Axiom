import { consoleApi, unwrap } from "@axiom/api";
import { useAdoptGrant, useStartSession } from "@axiom/app";
import { signInWithPasskey } from "../webauthn";

// A passkey completes a sign-in from the guest page, or finishes a password sign-in that is waiting for its second
// factor: the same assertion under the endpoints that bind the ceremony to the pending session.
const routes = {
  signIn: { begin: "/api/console/v1/auth/passkeys/begin", finish: "/api/console/v1/auth/passkeys/finish" },
  mfa: { begin: "/api/console/v1/auth/passkeys/mfa/begin", finish: "/api/console/v1/auth/passkeys/mfa/finish" },
} as const;

// usePasskey is a passkey request of the page (attempt.ts). Until the assertion is sent, the signal ends the challenge
// and closes the browser's prompt; the finish, which may set a cookie, is committed first, so it can no longer be
// cancelled, and the session it starts is the one the page then shows. As a second step, a failed read of that session
// is UnreadSession (useAdoptGrant): the step is not offered again.
export function usePasskey(kind: keyof typeof routes) {
  const signIn = useStartSession();
  const adopt = useAdoptGrant();
  const start = kind === "mfa" ? adopt : signIn;
  return async (signal: AbortSignal, commit: () => void) => {
    const { begin, finish } = routes[kind];
    const challenge = await unwrap(consoleApi.POST(begin, { body: {}, signal }));
    const credential = await signInWithPasskey(challenge.public_key, signal);
    signal.throwIfAborted();
    commit();
    const grant = await unwrap(
      consoleApi.POST(finish, { params: { query: { flow_id: challenge.flow_id } }, body: credential }),
    );
    await start(grant.csrf_token);
  };
}
