import { createContext, type ReactNode, use } from "react";

// Where Gate is sending the visitor: another sign-in step within the same flow ("auth"), or into the authenticated
// application ("member"), which an external host such as the website enters with a full page load.
export type SessionTarget = "auth" | "member";

// What Gate needs from its host's router: the current app-relative href, the validated return path on a sign-in flow
// page, and how to replace the current location.
export type SessionNavigation = {
  here: string;
  next: string | undefined;
  replace: (path: string, target: SessionTarget) => void;
};

const SessionNavigationContext = createContext<SessionNavigation | null>(null);

export function SessionNavigationProvider({ value, children }: { value: SessionNavigation; children: ReactNode }) {
  return <SessionNavigationContext value={value}>{children}</SessionNavigationContext>;
}

export function useSessionNavigation(): SessionNavigation {
  const navigation = use(SessionNavigationContext);
  if (!navigation) {
    throw new Error("useSessionNavigation is used outside a SessionNavigationProvider");
  }
  return navigation;
}
