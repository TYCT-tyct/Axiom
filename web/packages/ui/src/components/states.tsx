import { createContext, type ReactNode, useContext } from "react";
import { useTranslation } from "react-i18next";
import { useFormat } from "../format";
import { ActionButton } from "./action-button";
import type { GlyphName } from "./glyph";
import { usePageTitle } from "./page-title";
import { Placeholder, StateBlock } from "./state-block";

// Every view of data has four states besides showing it: loading, empty, failed, and not allowed (docs/frontend.md).
// DataState picks the right one from a query; each is the design's StateBlock.

// A failure that carries what the server said about it; the API client's errors have this shape. Status 0 means no
// response arrived.
type Failure = { status: number; requestId: string | null };

function isFailure(error: unknown): error is Failure {
  return typeof error === "object" && error !== null && "status" in error && "requestId" in error;
}

// A state that replaces the whole page (page) gives the page its heading and its title; any other is a section of one.
function StateFrame({
  icon,
  title,
  children,
  action,
  bad = false,
  requestId,
  page = false,
  compact = false,
}: {
  icon: GlyphName;
  title: string;
  children?: ReactNode;
  action?: ReactNode;
  bad?: boolean;
  requestId?: string | null;
  page?: boolean;
  compact?: boolean;
}) {
  usePageTitle(page ? title : undefined);
  return (
    <StateBlock
      icon={icon}
      title={title}
      level={page ? 1 : 2}
      bad={bad}
      compact={compact}
      actions={action}
      requestId={requestId}
    >
      {children}
    </StateBlock>
  );
}

// Where a view stands in its page: compact inside a section, or the whole page (page), which then has the heading.
type Placement = { compact?: boolean; page?: boolean };

// Loading is a state of the page like any other: one that replaces the page area keeps a level-one heading (#363).
export function LoadingState({ page }: Placement = {}) {
  const { t } = useTranslation("common");
  return <Placeholder label={t("state.loading")} page={page} />;
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
    <StateFrame icon="overview" title={title ?? t("state.emptyTitle")} action={action} {...placement}>
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
    <StateFrame icon="security" title={t("state.forbiddenTitle")} {...placement}>
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
      action={<a href={import.meta.env.BASE_URL}>{t("state.home")}</a>}
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
      icon="warn"
      bad
      title={t("state.errorTitle")}
      requestId={failure?.requestId}
      {...placement}
      action={
        onRetry ? (
          <ActionButton variant="pri" onClick={onRetry}>
            {t("state.retry")}
          </ActionButton>
        ) : null
      }
    >
      <p role="alert">{failure?.status === 0 ? t("state.networkError") : t("state.errorBody")}</p>
      {at ? <p>{t("state.failedAt", { time: format.dateTime(at) })}</p> : null}
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
      icon="warn"
      bad
      title={t("state.pageFailed")}
      page
      action={
        <ActionButton variant="pri" onClick={() => window.location.reload()}>
          {t("state.reload")}
        </ActionButton>
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
      action={<a href={import.meta.env.BASE_URL}>{t("state.home")}</a>}
      page
    >
      <p>{t("state.notFoundBody")}</p>
    </StateFrame>
  );
}
