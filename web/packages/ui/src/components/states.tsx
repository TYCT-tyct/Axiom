import { type ComponentType, createContext, type ReactNode, useContext } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "../cn";
import { useFormat } from "../format";
import { Button } from "./button";
import { Icon } from "./icon";
import { usePageTitle } from "./page-title";

// Every view of data has four states besides showing it: loading, empty, failed, and not allowed (docs/frontend.md).
// DataState picks the right one from a query.

// A failure that carries what the server said about it; the API client's errors have this shape. Status 0 means no
// response arrived.
type Failure = { status: number; requestId: string | null };

function isFailure(error: unknown): error is Failure {
  return typeof error === "object" && error !== null && "status" in error && "requestId" in error;
}

// What a state says, for the app's StateView to draw: the kind, the words, and the ways on (the state's own retry or
// way home, or an action the page gives).
export type StateFrameProps = {
  kind: "empty" | "forbidden" | "gone" | "failed";
  title: string;
  level: 1 | 2;
  compact: boolean;
  requestId?: string | null;
  // when is the failure's time, as the page shows it
  when?: string;
  retry?: { label: string; run: () => void };
  home?: { label: string; href: string };
  action?: ReactNode;
  children?: ReactNode;
};

// How an app draws the states. The console draws the design's StateBlock (DesignProvider); the admin keeps the frames
// below until its shell moves to the design (#747 A1), so its first screen does not load the design's.
export type StateView = { Frame: ComponentType<StateFrameProps>; Loading: ComponentType<{ page: boolean }> };

const icons = { empty: "grid", forbidden: "lock", gone: "search", failed: "alert" } as const;

function LegacyFrame({ kind, title, level, compact, requestId, when, retry, home, action, children }: StateFrameProps) {
  const { t } = useTranslation("common");
  const Heading = level === 1 ? "h1" : "h2";
  return (
    <div
      className={cn("mx-auto flex max-w-md flex-col items-center gap-3 px-4 text-center", compact ? "py-8" : "py-16")}
    >
      <span
        className={cn(
          "flex size-10 items-center justify-center rounded-full bg-s2",
          kind === "failed" ? "text-bad" : "text-t3",
        )}
      >
        <Icon name={icons[kind]} />
      </span>
      <Heading className="text-[15px] text-t1">{title}</Heading>
      {children ? (
        <div className="space-y-1 text-ui text-t3">
          {children}
          {requestId ? <p className="font-mono text-t4 select-all">{t("state.requestId", { id: requestId })}</p> : null}
          {when ? <p className="text-t4">{when}</p> : null}
        </div>
      ) : null}
      {retry ? (
        <Button onClick={retry.run}>
          <Icon name="refresh" />
          {retry.label}
        </Button>
      ) : null}
      {home ? (
        <a href={home.href} className="text-accent underline-offset-4 hover:underline">
          {home.label}
        </a>
      ) : null}
      {action}
    </div>
  );
}

const skeletonWidths = ["w-11/12", "w-4/5", "w-2/3", "w-1/2"];

// Loading that replaces the page area gives it a level-one heading, hidden here because the bars already say the same
// thing to a reader who is looking, so a page waiting for its data is one a reader and a check can still tell apart
// from a page that never rendered (#363). The heading sits outside the status the bars speak in: it marks the page,
// it is not news.
function LegacyLoading({ page }: { page: boolean }) {
  const { t } = useTranslation("common");
  const label = t("state.loading");
  return (
    <div className="p-6">
      {page ? <h1 className="sr-only">{label}</h1> : null}
      <div role="status" className="space-y-3">
        <span className="sr-only">{label}</span>
        {skeletonWidths.map((width) => (
          <div key={width} className={cn("h-5 animate-pulse rounded-xs bg-s2", width)} />
        ))}
      </div>
    </div>
  );
}

const StateViewContext = createContext<StateView>({ Frame: LegacyFrame, Loading: LegacyLoading });
export const StateViewProvider = StateViewContext.Provider;

// A state that replaces the whole page (page) gives the page its heading and its title; any other is a section of one.
function StateFrame({
  page = false,
  compact = false,
  ...said
}: Omit<StateFrameProps, "level" | "compact"> & Placement) {
  usePageTitle(page ? said.title : undefined);
  const { Frame } = useContext(StateViewContext);
  return <Frame {...said} level={page ? 1 : 2} compact={compact} />;
}

// Where a view stands in its page: compact inside a section, or the whole page (page), which then has the heading.
type Placement = { compact?: boolean; page?: boolean };

// Loading is a state of the page like any other: one that replaces the page area keeps a level-one heading (#363).
export function LoadingState({ page = false }: Placement = {}) {
  const { Loading } = useContext(StateViewContext);
  return <Loading page={page} />;
}

export function EmptyState({
  title,
  children,
  action,
  ...placement
}: {
  title?: string;
  children?: ReactNode;
  action?: ReactNode;
} & Placement) {
  const { t } = useTranslation("common");
  return (
    <StateFrame kind="empty" title={title ?? t("state.emptyTitle")} action={action} {...placement}>
      {children}
    </StateFrame>
  );
}

// What a person without the permission is told to do, in the words of the app they are in: who grants it differs by app,
// so the app gives the sentence (AppShell) and the shared state only places it.
const ForbiddenHintContext = createContext<string | undefined>(undefined);
export const ForbiddenHint = ForbiddenHintContext.Provider;

function ForbiddenState(placement: Placement) {
  const { t } = useTranslation("common");
  const hint = useContext(ForbiddenHintContext);
  return (
    <StateFrame kind="forbidden" title={t("state.forbiddenTitle")} {...placement}>
      <p>{t("state.forbiddenBody")}</p>
      {hint ? <p>{hint}</p> : null}
    </StateFrame>
  );
}

// The thing asked for is not there: asking again will not find it, so the way out is back home.
function GoneState(placement: Placement) {
  const { t } = useTranslation("common");
  return (
    <StateFrame
      kind="gone"
      title={t("state.goneTitle")}
      home={{ label: t("state.home"), href: import.meta.env.BASE_URL }}
      {...placement}
    >
      <p>{t("state.goneBody")}</p>
    </StateFrame>
  );
}

function ErrorState({
  error,
  at,
  onRetry,
  ...placement
}: {
  error: unknown;
  at?: Date;
  onRetry?: () => void;
} & Placement) {
  const { t } = useTranslation("common");
  const format = useFormat();
  const failure = isFailure(error) ? error : null;
  return (
    <StateFrame
      kind="failed"
      title={t("state.errorTitle")}
      requestId={failure?.requestId}
      when={at ? t("state.failedAt", { time: format.dateTime(at) }) : undefined}
      retry={onRetry ? { label: t("state.retry"), run: onRetry } : undefined}
      {...placement}
    >
      <p role="alert">{failure?.status === 0 ? t("state.networkError") : t("state.errorBody")}</p>
    </StateFrame>
  );
}

// The parts of a TanStack Query result that DataState reads.
type QueryLike<T> = {
  status: "pending" | "error" | "success";
  data: T | undefined;
  error: unknown;
  errorUpdatedAt: number;
  refetch: () => unknown;
};

export function DataState<T>({
  query,
  isEmpty,
  empty,
  children,
  ...placement
}: {
  query: QueryLike<T>;
  isEmpty?: (data: T) => boolean;
  empty?: ReactNode;
  children: (data: T) => ReactNode;
} & Placement) {
  if (query.status === "pending") {
    return <LoadingState page={placement.page} />;
  }
  if (query.status === "error" || query.data === undefined) {
    const status = isFailure(query.error) ? query.error.status : undefined;
    if (status === 403) {
      return <ForbiddenState {...placement} />;
    }
    if (status === 404) {
      return <GoneState {...placement} />;
    }
    return (
      <ErrorState
        error={query.error}
        at={new Date(query.errorUpdatedAt)}
        onRetry={() => query.refetch()}
        {...placement}
      />
    );
  }
  if (isEmpty?.(query.data)) {
    return empty ?? <EmptyState {...placement} />;
  }
  return children(query.data);
}

// PageFailedState replaces a page that could not render or load; reloading fetches the current build.
export function PageFailedState() {
  const { t } = useTranslation("common");
  return (
    <StateFrame
      kind="failed"
      title={t("state.pageFailed")}
      page
      retry={{ label: t("state.reload"), run: () => window.location.reload() }}
    >
      <p role="alert">{t("state.pageFailedBody")}</p>
    </StateFrame>
  );
}

// NotFoundState is shown for an address no page answers. Its link home is a plain link, so it needs no router and
// serves every app; loading the app afresh also picks up a newer build, if the address came from one.
export function NotFoundState() {
  const { t } = useTranslation("common");
  return (
    <StateFrame
      kind="gone"
      title={t("state.notFoundTitle")}
      home={{ label: t("state.home"), href: import.meta.env.BASE_URL }}
      page
    >
      <p>{t("state.notFoundBody")}</p>
    </StateFrame>
  );
}
