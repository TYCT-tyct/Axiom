import type { Schemas } from "@axiom/api";
import {
  Button,
  Cluster,
  CodeBlock,
  CopyButton,
  Dialog,
  FormError,
  Stack,
  SubmitButton,
  Text,
  TextField,
} from "@axiom/ui";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createContext, type ReactNode, use, useCallback, useEffect, useLayoutEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useErrors } from "./errors";
import { readForm } from "./forms";
import { QrCode } from "./qr";
import { claimSession, holdsIdentity, identityOf, useAuth, useCachedSession, useStartSession } from "./session";

type Change = Schemas["FactorChange"];

// Recovery codes exist only in the answer that makes them. The dialog that shows them does not close on Escape or a
// click outside, so they cannot be lost by accident, and it lives above the pages: changing a factor gives this device
// a new session, which can move the page underneath (a first factor ends the sign-in's setup step), and the codes must
// stay until the user has kept them. Keeping them adopts the new session; if that fails the codes stay, with the
// reason, and the button tries again.
function RecoveryCodes({ change, onClose }: { change: Change; onClose: () => void }) {
  const { t } = useTranslation("common");
  const errors = useErrors();
  const start = useStartSession();
  const keep = useMutation({ mutationFn: () => start(change.csrf_token), onSuccess: onClose });
  return (
    <Dialog title={t("security.codes.title")} onClose={onClose} dismissible={false}>
      <Stack>
        <Text tone="muted">{t("security.codes.body")}</Text>
        <CodeBlock as="ul">
          {change.recovery_codes.map((code) => (
            <li key={code}>{code}</li>
          ))}
        </CodeBlock>
        {keep.isError ? <FormError>{errors.message(keep.error)}</FormError> : null}
        <Cluster justify="between">
          <CopyButton text={change.recovery_codes.join("\n")} />
          <Button variant="primary" size="md" onClick={() => keep.mutate()} disabled={keep.isPending}>
            {t("security.codes.done")}
          </Button>
        </Cluster>
      </Stack>
    </Dialog>
  );
}

const ShowCodes = createContext<((change: Change, identity: string) => void) | null>(null);
const HideCodes = createContext<((hidden: boolean) => void) | null>(null);

// RecoveryCodesHost shows the recovery codes of a changed factor above everything in it. The codes belong to the person
// signed in: when the browser passes to another person (another tab signs out and someone else signs in), the dialog
// closes and the codes leave memory, because this host outlives the pages. The session itself rotating is how these
// codes were made, so the host follows the person, not the token. The caller names the person it asked for the change
// as, captured before the request went out: this host must not read the session when the answer lands, because by then
// another tab may have signed someone else in. While a page holds them hidden (useHideRecoveryCodes), the codes stay
// here, off screen, and come back when it goes, if the person is still the one who asked.
export function RecoveryCodesHost({ children }: { children: ReactNode }) {
  const identity = identityOf(useCachedSession());
  const [shown, setShown] = useState<{ change: Change; identity: string } | null>(null);
  const [hidden, setHidden] = useState(false);
  useEffect(() => {
    setShown((held) => (held && held.identity !== identity ? null : held));
  }, [identity]);
  const show = useCallback((change: Change, asked: string) => setShown({ change, identity: asked }), []);
  const visible = !hidden && shown && shown.identity === identity ? shown.change : null;
  return (
    <ShowCodes value={show}>
      <HideCodes value={setHidden}>{children}</HideCodes>
      {visible ? <RecoveryCodes change={visible} onClose={() => setShown(null)} /> : null}
    </ShowCodes>
  );
}

// useHideRecoveryCodes keeps the codes off screen while hidden holds: a page that may be open to another person than the
// one signed in (the console's reset page) holds them. A layout effect, so they are gone before that page is painted.
export function useHideRecoveryCodes(hidden: boolean) {
  const hide = use(HideCodes);
  if (!hide) {
    throw new Error("useHideRecoveryCodes is used outside a RecoveryCodesHost");
  }
  useLayoutEffect(() => {
    hide(hidden);
    return () => hide(false);
  }, [hide, hidden]);
}

function useShowCodes() {
  const show = use(ShowCodes);
  if (!show) {
    throw new Error("useShowCodes is used outside a RecoveryCodesHost");
  }
  return show;
}

// useShowRecoveryCodes shows the codes a factor change made, above the page, until the person says they have kept them.
// A page that caused the change calls it; the dialog adopts the new session when it closes.
export function useShowRecoveryCodes() {
  return useShowCodes();
}

// A factor change replaces the session it was asked under before its answer is read, so its codes belong to the person
// who asked, claimed when the request starts; an answer that lands after another person signed in is dropped.
function useFactorCodes<TVariables>(change: (variables: TVariables) => Promise<Change>, onClose: () => void) {
  const client = useQueryClient();
  const show = useShowCodes();
  return useMutation({
    mutationFn: change,
    onMutate: () => claimSession(client),
    onSuccess: (codes, _variables, claim) => {
      if (!claim || !holdsIdentity(client, claim.identity)) {
        return;
      }
      show(codes, claim.identity);
      onClose();
    },
  });
}

// Starting to enable asks for a new secret and opens the dialog that proves it; nothing is stored until it is proved.
export function useBeginTotp() {
  const { api } = useAuth();
  return useMutation({ mutationFn: api.beginTotpSetup });
}

// Enabling takes three steps in one dialog: scan the secret, prove it with the password and a code, keep the codes.
export function TotpSetupDialog({ setup, onClose }: { setup: Schemas["TotpSetup"]; onClose: () => void }) {
  const { t } = useTranslation("common");
  const errors = useErrors();
  const { api } = useAuth();
  const confirm = useFactorCodes(api.confirmTotpSetup, onClose);
  return (
    <Dialog title={t("security.setup.title")} onClose={onClose} dismissible={!confirm.isPending}>
      <Stack>
        <Text tone="muted">{t("security.setup.scan")}</Text>
        <Cluster gap={16}>
          <QrCode value={setup.uri} label={t("security.setup.qr")} />
          <Stack gap={8}>
            <Text size="sm" tone="faint">
              {t("security.setup.manual")}
            </Text>
            <CodeBlock>{setup.secret}</CodeBlock>
          </Stack>
        </Cluster>
        <form
          noValidate
          onSubmit={(event) => {
            event.preventDefault();
            const { password = "", code = "" } = readForm(event.currentTarget);
            if (!confirm.isPending) {
              confirm.mutate({ secret: setup.secret, password, code: code.trim() });
            }
          }}
        >
          <Stack>
            {confirm.isError ? <FormError>{errors.message(confirm.error)}</FormError> : null}
            <TextField
              label={t("auth.password")}
              name="password"
              type="password"
              autoComplete="current-password"
              error={errors.field(confirm.error, "password")}
            />
            <TextField
              label={t("security.setup.code")}
              name="code"
              inputMode="numeric"
              autoComplete="one-time-code"
              error={errors.field(confirm.error, "code")}
            />
            <Cluster justify="end">
              <Button size="md" onClick={onClose} disabled={confirm.isPending}>
                {t("cancel")}
              </Button>
              <SubmitButton pending={confirm.isPending}>{t("security.setup.enable")}</SubmitButton>
            </Cluster>
          </Stack>
        </form>
      </Stack>
    </Dialog>
  );
}

export function RecoveryDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation("common");
  const errors = useErrors();
  const { api } = useAuth();
  const regenerate = useFactorCodes(api.regenerateRecoveryCodes, onClose);
  return (
    <Dialog title={t("security.regenerate.title")} onClose={onClose} dismissible={!regenerate.isPending}>
      <form
        noValidate
        onSubmit={(event) => {
          event.preventDefault();
          if (!regenerate.isPending) {
            regenerate.mutate({ code: (readForm(event.currentTarget).code ?? "").trim() });
          }
        }}
      >
        <Stack>
          <Text tone="muted">{t("security.regenerate.body")}</Text>
          {regenerate.isError ? <FormError>{errors.message(regenerate.error)}</FormError> : null}
          <TextField
            label={t("security.setup.code")}
            name="code"
            inputMode="numeric"
            autoComplete="one-time-code"
            error={errors.field(regenerate.error, "code")}
          />
          <Cluster justify="end">
            <Button size="md" onClick={onClose} disabled={regenerate.isPending}>
              {t("cancel")}
            </Button>
            <SubmitButton pending={regenerate.isPending}>{t("security.regenerate.confirm")}</SubmitButton>
          </Cluster>
        </Stack>
      </form>
    </Dialog>
  );
}
