import { consoleApi, unwrap } from "@axiom/api";
import { useStartSession } from "@axiom/app";
import { signInWithPasskey } from "../webauthn";
import type { Attempt } from "./attempt";

// A passkey completes a sign-in from the guest page, or finishes a password sign-in that is waiting for its second
// factor: the same assertion under the endpoints that bind the ceremony to the pending session.
const routes = {
  signIn: { begin: "/api/console/v1/auth/passkeys/begin", finish: "/api/console/v1/auth/passkeys/finish" },
  mfa: { begin: "/api/console/v1/auth/passkeys/mfa/begin", finish: "/api/console/v1/auth/passkeys/mfa/finish" },
} as const;

// usePasskey runs a passkey attempt (attempt.ts). The challenge and the browser's prompt end with the attempt; the
// assertion is sent only while the attempt is still the page's, and once it is sent the session it may have started
// is adopted whatever became of the page, since the server may already have set its cookie.
export function usePasskey(kind: keyof typeof routes) {
  const start = useStartSession();
  return async (attempt: Attempt) => {
    try {
      const { begin, finish } = routes[kind];
      const challenge = await unwrap(consoleApi.POST(begin, { body: {}, signal: attempt.signal }));
      const credential = await signInWithPasskey(challenge.public_key, attempt.signal);
      if (!attempt.live()) return;
      const grant = await unwrap(
        consoleApi.POST(finish, { params: { query: { flow_id: challenge.flow_id } }, body: credential }),
      );
      await start(grant.csrf_token);
      attempt.done();
    } catch (error) {
      attempt.fail(error);
    }
  };
}
