import { Logo, PreferenceSwitches, usePageTitle } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { siteQuery } from "../queries";

// AuthFrame is the page around a sign-in, two-step or registration form: the site's name and logo above, one form
// below. It is its own page (main, heading, title), as the pages inside the console's shell are.
export function AuthFrame({
  title,
  description,
  footer,
  children,
}: {
  title: string;
  description?: string;
  footer?: ReactNode;
  children: ReactNode;
}) {
  usePageTitle(title);
  const site = useQuery(siteQuery);
  return (
    <div className="flex min-h-dvh flex-col">
      <header className="flex flex-wrap items-center justify-between gap-3 p-4 sm:px-6">
        <Logo name={site.data?.name} logoUrl={site.data?.logo_url} className="min-w-0" />
        <PreferenceSwitches className="flex gap-2" />
      </header>
      <main
        id="main"
        tabIndex={-1}
        className="mx-auto flex w-full max-w-sm flex-1 flex-col justify-center gap-6 px-4 pb-24 outline-none"
      >
        <div className="space-y-1.5">
          <h1 className="text-xl tracking-[-0.01em] text-t1">{title}</h1>
          {description ? <p className="text-ui text-t3">{description}</p> : null}
        </div>
        {children}
        {footer ? <p className="text-ui text-t3">{footer}</p> : null}
      </main>
    </div>
  );
}
