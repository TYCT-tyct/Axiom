import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "../cn";
import { useFormat } from "../format";
import { Button } from "./button";
import { Icon, type IconName } from "./icon";
import { usePageTitle } from "./page-title";

// Every view of data has four states besides showing it: loading, empty, failed, and not allowed (docs/frontend.md).
// DataState picks the right one from a query; the views are exported for screens that are not a single query.

// A failure that carries what the server said about it; the API client's errors have this shape. Status 0 means no
// response arrived.
export type Failure = { status: number; requestId: string | null };

function isFailure(error: unknown): error is Failure {
  return typeof error === "object" && error !== null && "status" in error && "requestId" in error;
}

// A state that replaces the whole page (page) gives the page its heading and its title; any other is a section of one.
function StateFrame({
  icon,
  title,
  children,
  action,
  tone = "text-t3",
  page = false,
}: {
  icon: IconName;
  title: string;
  children?: ReactNode;
  action?: ReactNode;
  tone?: string;
  page?: boolean;
}) {
  usePageTitle(page ? title : undefined);
  const Heading = page ? "h1" : "h2";
  return (
    <div className="mx-auto flex max-w-md flex-col items-center gap-3 px-4 py-16 text-center">
      <span className={cn("flex size-10 items-center justify-center rounded-full bg-s2", tone)}>
        <Icon name={icon} />
      </span>
      <Heading className="text-[15px] text-t1">{title}</Heading>
      {children ? <div className="space-y-1 text-ui text-t3">{children}</div> : null}
      {action}
    </div>
  );
}

const skeletonWidths = ["w-11/12", "w-4/5", "w-2/3", "w-1/2"];

export function LoadingState() {
  const { t } = useTranslation("common");
  return (
    <div role="status" className="space-y-3 p-6">
      <span className="sr-only">{t("state.loading")}</span>
      {skeletonWidths.map((width) => (
        <div key={width} className={cn("h-5 animate-pulse rounded-xs bg-s2", width)} />
      ))}
    </div>
  );
}

export function EmptyState({ title, children, action }: { title?: string; children?: ReactNode; action?: ReactNode }) {
  const { t } = useTranslation("common");
  return (
    <StateFrame icon="grid" title={title ?? t("state.emptyTitle")} action={action}>
      {children}
    </StateFrame>
  );
}

export function ForbiddenState() {
  const { t } = useTranslation("common");
  return (
    <StateFrame icon="lock" title={t("state.forbiddenTitle")}>
      <p>{t("state.forbiddenBody")}</p>
    </StateFrame>
  );
}

export function ErrorState({ error, at, onRetry }: { error: unknown; at?: Date; onRetry?: () => void }) {
  const { t } = useTranslation("common");
  const format = useFormat();
  const failure = isFailure(error) ? error : null;
  return (
    <StateFrame
      icon="alert"
      tone="text-bad"
      title={t("state.errorTitle")}
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
}: {
  query: QueryLike<T>;
  isEmpty?: (data: T) => boolean;
  empty?: ReactNode;
  children: (data: T) => ReactNode;
}) {
  if (query.status === "pending") {
    return <LoadingState />;
  }
  if (query.status === "error" || query.data === undefined) {
    if (isFailure(query.error) && query.error.status === 403) {
      return <ForbiddenState />;
    }
    return <ErrorState error={query.error} at={new Date(query.errorUpdatedAt)} onRetry={() => query.refetch()} />;
  }
  if (isEmpty?.(query.data)) {
    return empty ?? <EmptyState />;
  }
  return children(query.data);
}

// PageFailedState replaces a page that could not render or load; reloading fetches the current build.
export function PageFailedState() {
  const { t } = useTranslation("common");
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

// NotFoundState is shown for an address no page answers; action leads back, usually a link home.
export function NotFoundState({ action }: { action?: ReactNode }) {
  const { t } = useTranslation("common");
  return (
    <StateFrame icon="search" title={t("state.notFoundTitle")} action={action} page>
      <p>{t("state.notFoundBody")}</p>
    </StateFrame>
  );
}
