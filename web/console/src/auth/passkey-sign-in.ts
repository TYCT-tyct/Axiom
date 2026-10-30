import { consoleApi, unwrap } from "@axiom/api";
import { useStartSession } from "@axiom/app";
import { useState } from "react";
import { signInWithPasskey } from "../webauthn";

// A passkey completes a sign-in from the guest page, or finishes a password sign-in that is waiting for its second
// factor: the same assertion under the endpoints that bind the ceremony to the pending session.
const routes = {
  signIn: { begin: "/api/console/v1/auth/passkeys/begin", finish: "/api/console/v1/auth/passkeys/finish" },
  mfa: { begin: "/api/console/v1/auth/passkeys/mfa/begin", finish: "/api/console/v1/auth/passkeys/mfa/finish" },
} as const;

export function usePasskeySignIn(kind: keyof typeof routes) {
  const start = useStartSession();
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState<unknown>(null);
  async function run() {
    setPending(true);
    setFailure(null);
    try {
      const { begin, finish } = routes[kind];
      const challenge = await unwrap(consoleApi.POST(begin, { body: {} }));
      const credential = await signInWithPasskey(challenge.public_key);
      const grant = await unwrap(
        consoleApi.POST(finish, {
          params: { query: { flow_id: challenge.flow_id } },
          body: credential,
        }),
      );
      await start(grant.csrf_token);
    } catch (error) {
      setFailure(error);
    } finally {
      setPending(false);
    }
  }
  return { start: run, pending, failure };
}
