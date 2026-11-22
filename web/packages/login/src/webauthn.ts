// The browser's WebAuthn API takes buffers where the server's JSON has base64url strings, and answers with buffers
// where the server expects strings back. These two conversions are all the ceremony needs; the server verifies
// everything that matters.

type Options = { publicKey?: Record<string, unknown> };

function bytes(value: unknown): ArrayBuffer {
  if (typeof value !== "string") {
    throw new Error("expected a base64url string");
  }
  const padded = value
    .replace(/-/g, "+")
    .replace(/_/g, "/")
    .padEnd(Math.ceil(value.length / 4) * 4, "=");
  const raw = atob(padded);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i += 1) {
    out[i] = raw.charCodeAt(i);
  }
  return out.buffer;
}

function text(buffer: ArrayBuffer | null): string | null {
  if (!buffer) {
    return null;
  }
  let raw = "";
  for (const byte of new Uint8Array(buffer)) {
    raw += String.fromCharCode(byte);
  }
  return btoa(raw).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function creationOptions(options: unknown): PublicKeyCredentialCreationOptions {
  const publicKey = structuredClone((options as Options).publicKey) as Record<string, unknown>;
  if (!publicKey) {
    throw new Error("no creation options");
  }
  publicKey.challenge = bytes(publicKey.challenge);
  const user = publicKey.user as Record<string, unknown>;
  user.id = bytes(user.id);
  const exclude = (publicKey.excludeCredentials ?? []) as Record<string, unknown>[];
  publicKey.excludeCredentials = exclude.map((credential) => ({ ...credential, id: bytes(credential.id) }));
  return publicKey as unknown as PublicKeyCredentialCreationOptions;
}

function requestOptions(options: unknown): PublicKeyCredentialRequestOptions {
  const publicKey = structuredClone((options as Options).publicKey) as Record<string, unknown>;
  if (!publicKey) {
    throw new Error("no request options");
  }
  publicKey.challenge = bytes(publicKey.challenge);
  const allow = (publicKey.allowCredentials ?? []) as Record<string, unknown>[];
  publicKey.allowCredentials = allow.map((credential) => ({ ...credential, id: bytes(credential.id) }));
  return publicKey as unknown as PublicKeyCredentialRequestOptions;
}

// publicKeyCredential narrows what the browser returned, typed as the base Credential by lib.dom.
function publicKeyCredential(credential: Credential | null): PublicKeyCredential {
  if (!(credential instanceof PublicKeyCredential)) {
    throw new Error("not a public-key credential");
  }
  return credential;
}

// credentialFields is what both ceremonies report about the credential itself. The response is what differs: an
// attestation object for a new credential, an assertion signature for a sign-in.
function credentialFields(credential: PublicKeyCredential) {
  return {
    id: credential.id,
    rawId: text(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    clientExtensionResults: credential.getClientExtensionResults(),
  };
}

function created(credential: Credential | null): Record<string, unknown> {
  const key = publicKeyCredential(credential);
  const response = key.response as AuthenticatorAttestationResponse;
  return {
    ...credentialFields(key),
    response: {
      clientDataJSON: text(response.clientDataJSON),
      attestationObject: text(response.attestationObject),
      transports: response.getTransports?.(),
    },
  };
}

function asserted(credential: Credential | null): Record<string, unknown> {
  const key = publicKeyCredential(credential);
  const response = key.response as AuthenticatorAssertionResponse;
  return {
    ...credentialFields(key),
    response: {
      clientDataJSON: text(response.clientDataJSON),
      authenticatorData: text(response.authenticatorData),
      signature: text(response.signature),
      userHandle: text(response.userHandle),
    },
  };
}

// registerPasskey runs a creation ceremony and returns the credential JSON the server verifies.
export async function registerPasskey(options: unknown): Promise<Record<string, unknown>> {
  return created(await navigator.credentials.create({ publicKey: creationOptions(options) }));
}

// signInWithPasskey runs a discoverable assertion; mediation "conditional" lets the browser offer the passkey inside
// an email field, which the sign-in page does not use, so the explicit choice is the one this asks for. signal closes
// the browser's prompt when the attempt it belongs to ends.
export async function signInWithPasskey(options: unknown, signal?: AbortSignal): Promise<Record<string, unknown>> {
  return asserted(await navigator.credentials.get({ publicKey: requestOptions(options), signal }));
}
