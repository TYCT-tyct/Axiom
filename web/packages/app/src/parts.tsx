import { Button, Icon, Panel } from "@axiom/ui";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
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
// amount can be, so it wraps rather than pushing the page wider.
export function Stat({ label, value, note }: { label: string; value: string; note?: ReactNode }) {
  return (
    <Panel className="p-4">
      <p className="text-label text-t3">{label}</p>
      <p className="mt-1.5 text-xl font-medium tracking-[-0.01em] text-t1 tabular-nums [overflow-wrap:anywhere] sm:text-2xl">
        {value}
      </p>
      {note ? <p className="mt-1 text-ui text-t3">{note}</p> : null}
    </Panel>
  );
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
    </div>
  );
}
