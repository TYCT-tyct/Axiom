import { type ComponentType, createContext, type ReactNode, useContext } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "../cn";
import { useFormat } from "../format";
import { Button } from "./button";
import { Icon, type IconName } from "./icon";
import { usePageTitle } from "./page-title";

// Every view of data has four states besides showing it: loading, empty, failed, and not allowed (docs/frontend.md).
// DataState picks the right one from a query.

// How the console draws these states (DesignProvider): the design's StateBlock. Without it, the ones below, which the
// admin keeps until its shell moves to the design (#747 A1), so its first screen carries none of the design's.
export type StateDesign = {
  Frame: ComponentType<FrameProps>;
  Loading: ComponentType<Placement>;
  Error: ComponentType<ErrorProps>;
  PageFailed: ComponentType;
};
export const StateDesignContext = createContext<StateDesign | null>(null);

// A failure that carries what the server said about it; the API client's errors have this shape. Status 0 means no
// response arrived.
type Failure = { status: number; requestId: string | null };

export function isFailure(error: unknown): error is Failure {
  return typeof error === "object" && error !== null && "status" in error && "requestId" in error;
}

// A state that replaces the whole page (page) gives the page its heading and its title; any other is a section of one.
export type FrameProps = {
  icon: IconName;
  title: string;
  children?: ReactNode;
  action?: ReactNode;
  tone?: string;
  page?: boolean;
  compact?: boolean;
};

function StateFrame(props: FrameProps) {
  const { icon, title, children, action, tone = "text-t3", page = false, compact = false } = props;
  usePageTitle(page ? title : undefined);
  const design = useContext(StateDesignContext);
  if (design) {
    return <design.Frame {...props} />;
  }
  const Heading = page ? "h1" : "h2";
  return (
    <div
      className={cn("mx-auto flex max-w-md flex-col items-center gap-3 px-4 text-center", compact ? "py-8" : "py-16")}
    >
      <span className={cn("flex size-10 items-center justify-center rounded-full bg-s2", tone)}>
        <Icon name={icon} />
      </span>
      <Heading className="text-[15px] text-t1">{title}</Heading>
      {children ? <div className="space-y-1 text-ui text-t3">{children}</div> : null}
      {action}
    </div>
  );
}

// Where a view stands in its page: compact inside a section, or the whole page (page), which then has the heading.
export type Placement = { compact?: boolean; page?: boolean };

const skeletonWidths = ["w-11/12", "w-4/5", "w-2/3", "w-1/2"];

// Loading is a state of the page like any other: one that replaces the page area gives it a level-one heading, hidden
// here because the bars already say the same thing to a reader who is looking, so a page waiting for its data is one a
// reader and a check can still tell apart from a page that never rendered (#363). The heading sits outside the status
// the bars speak in: it marks the page, it is not news.
export function LoadingState({ page }: Placement = {}) {
  const { t } = useTranslation("common");
  const design = useContext(StateDesignContext);
  if (design) {
    return <design.Loading page={page} />;
  }
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
    <StateFrame icon="grid" title={title ?? t("state.emptyTitle")} action={action} {...placement}>
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
    <StateFrame icon="lock" title={t("state.forbiddenTitle")} {...placement}>
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
      icon="search"
      title={t("state.goneTitle")}
      action={
        <a href={import.meta.env.BASE_URL} className="text-accent underline-offset-4 hover:underline">
          {t("state.home")}
        </a>
      }
      {...placement}
    >
      <p>{t("state.goneBody")}</p>
    </StateFrame>
  );
}

export type ErrorProps = { error: unknown; at?: Date; onRetry?: () => void } & Placement;

function ErrorState({ error, at, onRetry, ...placement }: ErrorProps) {
  const { t } = useTranslation("common");
  const format = useFormat();
  const design = useContext(StateDesignContext);
  if (design) {
    return <design.Error error={error} at={at} onRetry={onRetry} {...placement} />;
  }
  const failure = isFailure(error) ? error : null;
  return (
    <StateFrame
      icon="alert"
      tone="text-bad"
      title={t("state.errorTitle")}
      {...placement}
      action={
        onRetry ? (
          <Button onClick={onRetry}>
            <Icon name="refresh" />
            {t("state.retry")}
          </Button>
        ) : null
      }
    >
      <p role="alert">{failure?.status === 0 ? t("state.networkError") : t("state.errorBody")}</p>
      {failure?.requestId ? (
        <p className="font-mono text-t4 select-all">{t("state.requestId", { id: failure.requestId })}</p>
      ) : null}
      {at ? <p className="text-t4">{t("state.failedAt", { time: format.dateTime(at) })}</p> : null}
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
  const design = useContext(StateDesignContext);
  if (design) {
    return <design.PageFailed />;
  }
  return (
    <StateFrame
      icon="alert"
      tone="text-bad"
      title={t("state.pageFailed")}
      page
      action={
        <Button onClick={() => window.location.reload()}>
          <Icon name="refresh" />
          {t("state.reload")}
        </Button>
      }
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
      icon="search"
      title={t("state.notFoundTitle")}
      action={
        <a href={import.meta.env.BASE_URL} className="text-accent underline-offset-4 hover:underline">
          {t("state.home")}
        </a>
      }
      page
    >
      <p>{t("state.notFoundBody")}</p>
    </StateFrame>
  );
}
