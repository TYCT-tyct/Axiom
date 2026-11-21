import { createContext, type ReactNode, useContext } from "react";
import { useTranslation } from "react-i18next";
import { board } from "../board-classes";
import { classes } from "../classes";
import { useFormat } from "../format";
import { ActionButton } from "./action-button";
import { Icon, type IconName } from "./icon";
import { usePageTitle } from "./page-title";
import s from "./states.module.css";
import { TextLink } from "./text-link";

// Every view of data has four states besides showing it: loading, empty, failed, and not allowed (docs/frontend.md).
// DataState picks the right one from a query; non-loading states render the board's .stb/.stbi/.stba/.stbid markup.

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
    <div className={classes(board.board, board.ax, board.ov, s.host)}>
      <div className={classes(board.stb, bad && board.err, compact && s.compact)}>
        <span className={board.stbi}>
          <Icon name={icon} />
        </span>
        <Heading className={page ? s.pageTitle : undefined}>{title}</Heading>
        {children ? <div className={s.body}>{children}</div> : null}
        {action ? <div className={board.stba}>{action}</div> : null}
      </div>
    </div>
  );
}

type Placement = { compact?: boolean; page?: boolean };

const skeletonWidths = ["w92", "w80", "w67", "w50"] as const;

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

const ForbiddenHintContext = createContext<string | undefined>(undefined);
export const ForbiddenHint = ForbiddenHintContext.Provider;

export function ForbiddenState(placement: Placement = {}) {
  const { t } = useTranslation("common");
  const hint = useContext(ForbiddenHintContext);
  return (
    <StateFrame icon="lock" title={t("state.forbiddenTitle")} {...placement}>
      <p>{t("state.forbiddenBody")}</p>
      {hint ? <p>{hint}</p> : null}
    </StateFrame>
  );
}

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
      {failure?.requestId ? (
        <p className={classes(board.stbid, s.requestId)}>{t("state.requestId", { id: failure.requestId })}</p>
      ) : null}
      {at ? <p className={s.faint}>{t("state.failedAt", { time: format.dateTime(at) })}</p> : null}
    </StateFrame>
  );
}

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
