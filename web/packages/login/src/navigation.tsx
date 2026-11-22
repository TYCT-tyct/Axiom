import { type AnchorHTMLAttributes, type ComponentType, createContext, type ReactNode, use } from "react";

// The six sign-in flow paths owned by the Login board.
export type LoginPath = "/sign-in" | "/register" | "/mfa" | "/verify-email" | "/reset-password" | "/consent";

export type LoginSearch = {
  next?: string;
  invite?: string;
  oauth?: string;
};

export type LoginNavigateOptions = {
  search?: LoginSearch;
  replace?: boolean;
};

export type LoginLinkProps = Omit<AnchorHTMLAttributes<HTMLAnchorElement>, "href"> & {
  to: LoginPath;
  search?: LoginSearch;
};

// What a host (the console's TanStack Router, or a non-router host such as the website) supplies to the sign-in pages
// so they never import a router directly. Link must render a real <a href> so open-in-new-tab still works.
export type LoginNavigation = {
  search: LoginSearch;
  navigate: (to: LoginPath, options?: LoginNavigateOptions) => void;
  Link: ComponentType<LoginLinkProps>;
};

const LoginNavigationContext = createContext<LoginNavigation | null>(null);

export function LoginNavigationProvider({ value, children }: { value: LoginNavigation; children: ReactNode }) {
  return <LoginNavigationContext value={value}>{children}</LoginNavigationContext>;
}

export function useLoginNavigation(): LoginNavigation {
  const navigation = use(LoginNavigationContext);
  if (!navigation) {
    throw new Error("useLoginNavigation is used outside a LoginNavigationProvider");
  }
  return navigation;
}
