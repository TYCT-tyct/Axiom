import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { SignInPage, useErrors } from "@axiom/app";
import { Button, FormError, Icon } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, useSearch } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { siteQuery } from "../queries";
import { usePasskeySignIn } from "./passkey-sign-in";

export default function SignIn() {
  const { t } = useTranslation();
  const search = useSearch({ strict: false });
  const next = search.next;
  const site = useQuery(siteQuery);
  const origin = site.data?.login_origin;
  const methods = site.data;

  // A sign-in domain owns the provider callback and the passkey relying party. On another console domain the
  // password still works, but the visitors who need those two are sent to the domain that has them.
  useEffect(() => {
    if (!origin || !methods || window.location.origin === new URL(origin).origin) {
      return;
    }
    if ((methods.oauth_providers?.length ?? 0) > 0 || methods.passkeys) {
      window.location.replace(origin.replace(/\/$/, "") + window.location.pathname + window.location.search);
    }
  }, [origin, methods]);

  return (
    <SignInPage
      brand={site.data}
      description={t("auth.signIn.description")}
      extra={<Methods />}
      footer={
        <>
          {t("auth.signIn.noAccount")}{" "}
          <Link
            to="/register"
            search={{ next, invite: undefined }}
            className="text-accent underline underline-offset-4"
          >
            {t("auth.signIn.register")}
          </Link>
        </>
      }
    />
  );
}

// Methods are the ways in besides the password. A provider is a browser round trip; a passkey is a WebAuthn assertion
// the server checks against the public key it stored.
function Methods() {
  const { t } = useTranslation();
  const errors = useErrors();
  const site = useQuery(siteQuery);
  const search = useSearch({ strict: false });
  const oauth = useOAuthStart();
  const passkey = usePasskeySignIn("signIn");
  const providers = site.data?.oauth_providers ?? [];
  const passkeys = site.data?.passkeys ?? false;
  if (!passkeys && providers.length === 0 && search.oauth !== "failed") {
    return null;
  }
  return (
    <div className="mt-6 space-y-3">
      {search.oauth === "failed" ? <FormError>{t("auth.signIn.oauthFailed")}</FormError> : null}
      {oauth.failure ? <FormError>{errors.message(oauth.failure)}</FormError> : null}
      {passkey.failure ? <FormError>{errors.message(passkey.failure)}</FormError> : null}
      {providers.length > 0 || passkeys ? (
        <p className="text-center text-label text-t3">{t("auth.signIn.or")}</p>
      ) : null}
      {providers.map((provider) => (
        <ProviderButton key={provider} provider={provider} pending={oauth.pending} onStart={oauth.start} />
      ))}
      {passkeys ? <PasskeyButton pending={passkey.pending} onStart={passkey.start} /> : null}
    </div>
  );
}

function ProviderButton({
  provider,
  pending,
  onStart,
}: {
  provider: Schemas["OAuthProvider"];
  pending: boolean;
  onStart: (provider: Schemas["OAuthProvider"]) => Promise<void>;
}) {
  const { t } = useTranslation();
  return (
    <Button
      type="button"
      size="md"
      variant="secondary"
      className="w-full"
      disabled={pending}
      onClick={() => void onStart(provider)}
    >
      <Icon name="key" />
      {t(`auth.signIn.providers.${provider}`)}
    </Button>
  );
}

function PasskeyButton({ pending, onStart }: { pending: boolean; onStart: () => Promise<void> }) {
  const { t } = useTranslation();
  return (
    <Button
      type="button"
      size="md"
      variant="secondary"
      className="w-full"
      disabled={pending}
      onClick={() => void onStart()}
    >
      <Icon name="shield" />
      {t("auth.signIn.passkey")}
    </Button>
  );
}

function useOAuthStart() {
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState<unknown>(null);
  async function start(provider: Schemas["OAuthProvider"]) {
    setPending(true);
    setFailure(null);
    try {
      const { authorize_url } = await unwrap(
        consoleApi.POST("/api/console/v1/auth/oauth/start", { body: { provider } }),
      );
      window.location.assign(authorize_url);
    } catch (error) {
      setFailure(error);
      setPending(false);
    }
  }
  return { start, pending, failure };
}
