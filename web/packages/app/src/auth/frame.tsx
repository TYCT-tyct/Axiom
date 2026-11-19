import { Button, Logo, PreferenceSwitches, useBrandStyle, usePageTitle } from "@axiom/ui";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { SignOutError } from "../parts";
import { useSignOut } from "../session";

// What names the site on a page of the app: its name and logo, which the app's site settings give (none: the default),
// and the console's brand colour. The console reads them from getSite and the admin from its public getAdminSite, both
// before a session exists; the admin has no brand colour to read.
export type Brand = { name?: string; logo_url?: string; brand_color?: string } | undefined;

// AuthFrame is the page around a sign-in, two-step or registration form: the site's name and logo above, one form
// below. It is its own page (main, heading, title), as the pages inside an app's shell are.
export function AuthFrame({
  title,
  description,
  footer,
  brand,
  children,
  wide = false,
}: {
  title: string;
  description?: string;
  footer?: ReactNode;
  brand?: Brand;
  children: ReactNode;
  wide?: boolean;
}) {
  usePageTitle(title);
  useBrandStyle(brand?.brand_color);
  return (
    <div className="flex min-h-dvh flex-col">
      <header className="flex flex-wrap items-center justify-between gap-3 p-4 sm:px-6">
        <Logo name={brand?.name} logoUrl={brand?.logo_url} className="min-w-0" />
        <PreferenceSwitches className="flex gap-2" />
      </header>
      <main
        id="main"
        tabIndex={-1}
        className={`mx-auto flex w-full flex-1 flex-col justify-center gap-6 px-4 pb-24 outline-none ${
          wide ? "max-w-2xl" : "max-w-sm"
        }`}
      >
        <div className="space-y-1.5">
          <h1 className="text-xl tracking-[-0.01em] text-t1">{title}</h1>
          {description ? <p className="text-ui text-t3 [overflow-wrap:anywhere]">{description}</p> : null}
        </div>
        {children}
        {footer ? <p className="text-ui text-t3">{footer}</p> : null}
      </main>
    </div>
  );
}

// OtherAccount ends the session of a visitor who stopped halfway, to start again as someone else.
export function OtherAccount() {
  const { t } = useTranslation("common");
  const signOut = useSignOut();
  return (
    <>
      <Button variant="ghost" onClick={() => signOut.mutate()} disabled={signOut.isPending}>
        {t("auth.mfa.otherAccount")}
      </Button>
      <SignOutError error={signOut.error} />
    </>
  );
}
