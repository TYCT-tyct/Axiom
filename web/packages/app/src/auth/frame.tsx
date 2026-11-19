import { AuthPage, Button, useBrandStyle } from "@axiom/ui";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { SignOutError } from "../parts";
import { useSignOut } from "../session";

// What names the site on a page of the app: its name and logo, which the app's site settings give (none: the default),
// and the console's brand colour. The console reads them from getSite and the admin from its public getAdminSite, both
// before a session exists; the admin has no brand colour to read.
export type Brand = { name?: string; logo_url?: string; brand_color?: string } | undefined;

// AuthFrame is the page around a sign-in or two-step form (@axiom/ui AuthPage), in the site's brand.
export function AuthFrame({
  title,
  description,
  footer,
  brand,
  children,
}: {
  title: string;
  description?: string;
  footer?: ReactNode;
  brand?: Brand;
  children: ReactNode;
}) {
  useBrandStyle(brand?.brand_color);
  return (
    <AuthPage title={title} description={description} footer={footer} name={brand?.name} logoUrl={brand?.logo_url}>
      {children}
    </AuthPage>
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
