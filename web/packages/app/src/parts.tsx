import { Button, Icon, Panel } from "@axiom/ui";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useErrors } from "./errors";
import { useSession, useSignOut } from "./session";

// LoadMore asks for the next page of a list, which is only there while the last page gave a cursor.
export function LoadMore({
  list,
}: {
  list: { hasNextPage: boolean; isFetchingNextPage: boolean; fetchNextPage: () => unknown };
}) {
  const { t } = useTranslation("common");
  if (!list.hasNextPage) {
    return null;
  }
  return (
    <div className="border-t border-b1 p-3 text-center">
      <Button onClick={() => list.fetchNextPage()} disabled={list.isFetchingNextPage}>
        {list.isFetchingNextPage ? t("loadingMore") : t("loadMore")}
      </Button>
    </div>
  );
}

// Stat is one figure the user looks at first: what it is, then the number, large. The number may be as long as an
// amount can be, so it wraps rather than pushing the page wider, filling each line: breaking only where a word may would
// leave a minus sign alone on the first line.
export function Stat({ label, value, note }: { label: string; value: ReactNode; note?: ReactNode }) {
  return (
    <Panel className="p-4">
      <p className="text-label text-t3">{label}</p>
      <p className="mt-1.5 text-xl font-medium tracking-[-0.01em] text-t1 tabular-nums break-all sm:text-2xl">
        {value}
      </p>
      {note ? <p className="mt-1 text-ui text-t3">{note}</p> : null}
    </Panel>
  );
}

// Signing out that did not happen leaves the session open; the reason and that fact are said next to the button, which
// stays there to try again.
export function SignOutError({ error }: { error: unknown }) {
  const { t } = useTranslation("common");
  const errors = useErrors();
  return error ? (
    <span role="alert" className="block text-label text-bad [overflow-wrap:anywhere]">
      {t("signOutFailed", { reason: errors.message(error) })}
    </span>
  ) : null;
}

// SignedInAs says who is signed in and ends the session; it sits at the foot of an app's sidebar.
export function SignedInAs() {
  const { t } = useTranslation("common");
  const session = useSession();
  const signOut = useSignOut();
  return (
    <div className="space-y-1 border-t border-b1 pt-3">
      <p className="truncate px-2 text-label text-t3" title={session.user.email}>
        {session.user.email}
      </p>
      <Button
        variant="ghost"
        className="w-full justify-start"
        onClick={() => signOut.mutate()}
        disabled={signOut.isPending}
      >
        <Icon name="logout" />
        {t("nav.signOut")}
      </Button>
      <div className="px-2">
        <SignOutError error={signOut.error} />
      </div>
    </div>
  );
}
