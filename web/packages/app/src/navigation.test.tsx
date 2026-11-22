// @vitest-environment jsdom
import type { AuthApi } from "@axiom/api/auth";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Gate, SessionNavigationProvider, type SessionTarget, safeNext } from "./index";
import type { SessionNavigation } from "./navigation";
import { AuthProvider, type Session } from "./session";
import { TanStackSessionNavigation } from "./tanstack";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const member = (over: Partial<Session> = {}) =>
  ({
    user: { id: "user_a", email: "a@example.com", invite_code: "invite" },
    account: "acct_a",
    level: "mfa",
    mfa_enabled: true,
    csrf_token: "csrf-1",
    expires_at: "2026-10-15T00:00:00Z",
    permissions: [],
    ...over,
  }) as Session;

const unused = async () => {
  throw new Error("unused");
};

const stubApi = (session: Session | null): AuthApi => ({
  session: async () => {
    if (!session) throw new Error("unexpected");
    return session;
  },
  signIn: unused,
  verifyMfa: unused,
  signOut: async () => undefined,
  beginTotpSetup: unused,
  confirmTotpSetup: unused,
  regenerateRecoveryCodes: unused,
  version: unused,
});

const roots: Root[] = [];
afterEach(async () => {
  await act(async () => {
    for (const root of roots.splice(0)) root.unmount();
  });
});

async function mount(node: ReactNode, session: Session | null) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(["session"], session);
  const root = createRoot(document.createElement("div"));
  roots.push(root);
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <AuthProvider api={stubApi(session)} requiresMfa={false} requiresConsent>
          {node}
        </AuthProvider>
      </QueryClientProvider>,
    );
  });
}

describe("Gate with SessionNavigationProvider", () => {
  it("reports target='member' when a guest page promotes into the app, and target='auth' between sign-in steps", async () => {
    const calls: [string, SessionTarget][] = [];
    const replace = vi.fn((path: string, target: SessionTarget) => calls.push([path, target]));

    await mount(
      <SessionNavigationProvider value={{ here: "/sign-in?next=%2Fkeys", next: "/keys", replace }}>
        <Gate stage="guest">
          <p />
        </Gate>
      </SessionNavigationProvider>,
      member(),
    );
    expect(calls).toEqual([["/keys", "member"]]);

    calls.length = 0;
    await mount(
      <SessionNavigationProvider value={{ here: "/sign-in?next=%2Fkeys", next: "/keys", replace }}>
        <Gate stage="guest">
          <p />
        </Gate>
      </SessionNavigationProvider>,
      member({ level: "pending_mfa" }),
    );
    expect(calls).toEqual([["/mfa?next=%2Fkeys", "auth"]]);

    calls.length = 0;
    await mount(
      <SessionNavigationProvider value={{ here: "/keys?x=1", next: undefined, replace }}>
        <Gate stage="member">
          <p />
        </Gate>
      </SessionNavigationProvider>,
      null,
    );
    expect(calls).toEqual([["/sign-in?next=%2Fkeys%3Fx%3D1", "auth"]]);
  });
});

type SessionNavigationReplace = SessionNavigation["replace"];

describe("Gate on the way in", () => {
  // the gate's page in a container, with the cache it reads, so a test can change the session under it
  async function shown(stage: "guest" | "member", session: Session | null, replace: SessionNavigationReplace) {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
    });
    client.setQueryData(["session"], session);
    const container = document.createElement("div");
    const root = createRoot(container);
    roots.push(root);
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <AuthProvider api={stubApi(session)} requiresMfa={false} requiresConsent>
            <SessionNavigationProvider value={{ here: "/sign-in", next: undefined, replace }}>
              <Gate stage={stage}>
                <p>{"the page"}</p>
              </Gate>
            </SessionNavigationProvider>
          </AuthProvider>
        </QueryClientProvider>,
      );
    });
    return { client, container };
  }

  // the cache tells its observers on the next task (notifyManager), after act's microtasks
  const changed = (change: () => void) =>
    act(async () => {
      change();
      await new Promise((done) => setTimeout(done, 0));
    });

  it("keeps a sign-in step on screen while it sends its new member on, but not a page left for another stage", async () => {
    const replace = vi.fn();
    const signIn = await shown("guest", null, replace);
    await changed(() => signIn.client.setQueryData(["session"], member()));
    expect(replace).toHaveBeenCalledWith("/", "member");
    expect(signIn.container.textContent).toBe("the page");

    replace.mockClear();
    const memberPage = await shown("member", member(), replace);
    await changed(() => memberPage.client.setQueryData(["session"], null));
    expect(replace).toHaveBeenCalledWith("/sign-in?next=%2Fsign-in", "auth");
    expect(memberPage.container.textContent).toBe("");
  });
});

describe("TanStackSessionNavigation", () => {
  it("reads validated leaf search.next and replaces under the router basepath", async () => {
    const root = createRootRoute({ component: Outlet });
    const login = createRoute({ getParentRoute: () => root, id: "login", component: Outlet });
    const signIn = createRoute({
      getParentRoute: () => login,
      path: "/sign-in",
      validateSearch: (search: Record<string, unknown>) => ({ next: safeNext(search.next, "/console/") }),
      component: () => (
        <Gate stage="guest">
          <p />
        </Gate>
      ),
    });
    const keys = createRoute({ getParentRoute: () => root, path: "/keys", component: () => <p /> });
    const home = createRoute({ getParentRoute: () => root, path: "/", component: () => <p /> });
    const routeTree = root.addChildren([login.addChildren([signIn]), keys, home]);

    const valid = createRouter({
      routeTree,
      basepath: "/console/",
      history: createMemoryHistory({ initialEntries: ["/console/sign-in?next=%2Fkeys"] }),
    });
    await mount(
      <TanStackSessionNavigation router={valid}>
        <RouterProvider router={valid} />
      </TanStackSessionNavigation>,
      member(),
    );
    expect(valid.history.location.href).toBe("/console/keys");

    const invalid = createRouter({
      routeTree,
      basepath: "/console/",
      history: createMemoryHistory({ initialEntries: ["/console/sign-in?next=//evil.example/"] }),
    });
    await mount(
      <TanStackSessionNavigation router={invalid}>
        <RouterProvider router={invalid} />
      </TanStackSessionNavigation>,
      member(),
    );
    expect(invalid.history.location.href).toBe("/console/");
  });
});
