import { createContext, type ReactNode, useContext } from "react";
import { useTranslation } from "react-i18next";
import { classes } from "../classes";
import { useFormat } from "../format";
import { ActionButton } from "./action-button";
import { Icon, type IconName } from "./icon";
import { usePageTitle } from "./page-title";
import s from "./states.module.css";
import { TextLink } from "./text-link";

// Every view of data has four states besides showing it: loading, empty, failed, and not allowed (docs/frontend.md).
// DataState picks the right one from a query.

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
  page = false,
  compact = false,
}: {
  icon: IconName;
  title: string;
  children?: ReactNode;
  action?: ReactNode;
  bad?: boolean;
  page?: boolean;
  compact?: boolean;
}) {
  usePageTitle(page ? title : undefined);
  const Heading = page ? "h1" : "h2";
  return (
    <div className={classes(s.frame, compact && s.compact)}>
      <span className={classes(s.mark, bad && s.bad)}>
        <Icon name={icon} />
      </span>
      <Heading className={s.title}>{title}</Heading>
      {children ? <div className={s.body}>{children}</div> : null}
      {action}
    </div>
  );
}

// Where a view stands in its page: compact inside a section, or the whole page (page), which then has the heading.
type Placement = { compact?: boolean; page?: boolean };

const skeletonWidths = ["w92", "w80", "w67", "w50"] as const;

// Loading is a state of the page like any other: one that replaces the page area gives it a level-one heading, hidden
// here because the bars already say the same thing to a reader who is looking, so a page waiting for its data is one a
// reader and a check can still tell apart from a page that never rendered (#363). The heading sits outside the status
// the bars speak in: it marks the page, it is not news.
export function LoadingState({ page }: Placement = {}) {
  const { t } = useTranslation("common");
  const label = t("state.loading");
  return (
    <div className={s.loading}>
      {page ? <h1 className={s.srOnly}>{label}</h1> : null}
      <div role="status" className={s.bars}>
        <span className={s.srOnly}>{label}</span>
        {skeletonWidths.map((width) => (
          <div key={width} className={classes(s.bar, s[width])} />
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
        <TextLink>
          <a href={import.meta.env.BASE_URL}>{t("state.home")}</a>
        </TextLink>
      }
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
      icon="alert"
      bad
      title={t("state.errorTitle")}
      {...placement}
      action={
        onRetry ? (
          <ActionButton size="sm" icon="refresh" onClick={onRetry}>
            {t("state.retry")}
          </ActionButton>
        ) : null
      }
    >
      <p role="alert">{failure?.status === 0 ? t("state.networkError") : t("state.errorBody")}</p>
      {failure?.requestId ? <p className={s.requestId}>{t("state.requestId", { id: failure.requestId })}</p> : null}
      {at ? <p className={s.faint}>{t("state.failedAt", { time: format.dateTime(at) })}</p> : null}
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

// PageFailedState replaces a page that could not render or load; reloading fetches the current build. A page whose way
// back is not a reload says its own (body).
export function PageFailedState({ body }: { body?: string }) {
  const { t } = useTranslation("common");
  return (
    <StateFrame
      icon="alert"
      bad
      title={t("state.pageFailed")}
      page
      action={
        <ActionButton size="sm" icon="refresh" onClick={() => window.location.reload()}>
          {t("state.reload")}
        </ActionButton>
      }
    >
      <p role="alert">{body ?? t("state.pageFailedBody")}</p>
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
        <TextLink>
          <a href={import.meta.env.BASE_URL}>{t("state.home")}</a>
        </TextLink>
      }
      page
    >
      <p>{t("state.notFoundBody")}</p>
    </StateFrame>
  );
}
