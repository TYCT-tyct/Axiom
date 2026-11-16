import { ApiError, refused } from "@axiom/api";
import { useEffect, useRef, useState } from "react";

// The two ways a console form writes (docs/frontend.md「写入策略」). The contract has no PATCH and a PUT replaces the
// whole object, so a form holds a draft of the whole object and sends it once on save (useExplicitSave). Only a body
// that is a single value, a body carrying the version it was read at, and the notification preferences written one
// after another may save each change as it is made (useLiveSave). The business object picks the strategy; EditPanel
// only shows it. The admin writes through ChangeDialog, never these.

// Drafts are plain form data, so two are the same when they serialise the same: a change typed and typed back is no
// change.
const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);

export type ExplicitSave<T, R = T> = {
  mode: "explicit";
  draft: T;
  set: (next: T) => void;
  // dirty is a draft that differs from what was last saved (or loaded): closing would lose it.
  dirty: boolean;
  saving: boolean;
  // failure is why the last save failed; the draft stays as it was, so saving again sends the same request.
  failure: unknown;
  // save sends the whole draft once; called again while it is out, it is the same request, not a second one. It
  // resolves with the answer, or undefined when the save failed.
  save: () => Promise<{ saved: R } | undefined>;
};

export function useExplicitSave<T, R = T>({
  initial,
  save,
}: {
  initial: T;
  save: (draft: T) => Promise<R>;
}): ExplicitSave<T, R> {
  const [base, setBase] = useState(initial);
  const [draft, setDraft] = useState(initial);
  const [state, setState] = useState<{ saving: boolean; failure: unknown }>({ saving: false, failure: null });
  const out = useRef<Promise<{ saved: R } | undefined>>(null);
  const run = () => {
    if (!out.current) {
      const sent = draft;
      setState({ saving: true, failure: null });
      out.current = save(sent)
        .then(
          (saved) => {
            setBase(sent);
            setState({ saving: false, failure: null });
            return { saved };
          },
          (failure: unknown) => {
            setState({ saving: false, failure });
            return undefined;
          },
        )
        .finally(() => {
          out.current = null;
        });
    }
    return out.current;
  };
  return {
    mode: "explicit",
    draft,
    set: setDraft,
    dirty: !same(draft, base),
    saving: state.saving,
    failure: state.failure,
    save: run,
  };
}

export type LiveSave<T> = {
  mode: "live";
  // value is the object the server last returned with the changes still on their way applied to it.
  value: T;
  // change saves one edit. Edits are sent one at a time, each applied to the object the previous write returned, so
  // two quick changes both land and neither is sent on a stale copy. While locked, a change is not taken.
  change: (edit: (current: T) => T) => void;
  saving: boolean;
  // saved is the last write succeeding, until the next change.
  saved: boolean;
  // failed is why a write failed and where that left the object; the edits queued behind it are dropped.
  failed: Failed | null;
  // locked is the object being read again after a 409, or a write whose result is unknown: no edit may be sent on a
  // copy that may be wrong until the page is reloaded.
  locked: boolean;
};

// After a failed write: reverted when the server refused it (a 4xx, so nothing was done) and value is the object as
// it was; reloaded when someone else changed the object (409 stale_revision, so nothing was done either) and it was
// read again; unconfirmed when the write may still have been done, or the reading again failed, so nothing more can be
// sent.
export type Failed = { error: unknown; after: "reverted" | "reloaded" | "unconfirmed" };

type View<T> = Omit<LiveSave<T>, "mode" | "change">;

type LiveSaveOptions<T> = {
  initial: T;
  // write sends the object with the edit applied (the whole body, or the one value an endpoint takes) and returns
  // the object as the server now has it.
  write: (next: T) => Promise<T>;
  // reread fetches the object as the server has it now, after a 409 stale_revision.
  reread: () => Promise<T>;
};

export function useLiveSave<T>({ initial, write, reread }: LiveSaveOptions<T>): LiveSave<T> {
  const confirmed = useRef(initial);
  const queue = useRef<((current: T) => T)[]>([]);
  const locked = useRef(false);
  const viewOf = (outcome: Partial<View<T>>): View<T> => ({
    value: queue.current.reduce((value, edit) => edit(value), confirmed.current),
    saving: queue.current.length > 0,
    saved: false,
    failed: null,
    locked: locked.current,
    ...outcome,
  });
  const [view, setView] = useState(() => viewOf({}));
  const publish = (outcome: Partial<View<T>>) => setView(viewOf(outcome));

  // Once the page that made the edits is gone (another page, another session), what is still queued is dropped.
  const gone = useGone(() => {
    queue.current = [];
  });

  // send sends one edit and settles what its answer means, unless the page went while it was out.
  const send = async (edit: (current: T) => T) => {
    try {
      confirmed.current = await write(edit(confirmed.current));
      if (!gone.current) {
        queue.current.shift();
        publish({ saved: queue.current.length === 0 });
      }
    } catch (error) {
      queue.current = [];
      if (gone.current) {
        return;
      }
      const result = await afterFailure(error, reread, () => {
        locked.current = true;
        publish({ saving: true });
      });
      if ("current" in result) {
        confirmed.current = result.current;
        locked.current = false;
      }
      publish({ failed: result.failed });
    }
  };

  // One drain runs while the queue holds anything: an edit pushed onto an empty queue starts it, and it ends when the
  // queue is empty (an edit is not taken while the object is read again, since that locks it).
  const drain = async () => {
    for (let edit = queue.current[0]; edit && !gone.current; edit = queue.current[0]) {
      await send(edit);
    }
  };

  return {
    mode: "live",
    ...view,
    change: (edit) => {
      if (locked.current) {
        return;
      }
      queue.current.push(edit);
      publish({});
      if (queue.current.length === 1) {
        void drain();
      }
    },
  };
}

// useGone is set once the component has unmounted, when drop runs, and cleared when it mounts, which StrictMode's
// remount would otherwise leave set.
function useGone(drop: () => void) {
  const gone = useRef(false);
  useEffect(() => {
    gone.current = false;
    return () => {
      gone.current = true;
      drop();
    };
  }, []);
  return gone;
}

// afterFailure says where a failed write left the object. A refusal proves nothing was done, and so does a stale
// revision, after which the object is read again (reading holds edits off meanwhile). Anything else (no answer, a 5xx,
// an unreadable response) may have been done, or may still be: a timed-out write can land after a read that comes back
// first, so no read confirms it, and nothing more is sent on any copy until the page is reloaded.
async function afterFailure<T>(
  error: unknown,
  reread: () => Promise<T>,
  reading: () => void,
): Promise<{ failed: Failed } | { failed: Failed; current: T }> {
  if (!refused(error)) {
    reading();
    return { failed: { error, after: "unconfirmed" } };
  }
  if (!(error instanceof ApiError && error.code === "stale_revision")) {
    return { failed: { error, after: "reverted" } };
  }
  reading();
  try {
    return { failed: { error, after: "reloaded" }, current: await reread() };
  } catch {
    return { failed: { error, after: "unconfirmed" } };
  }
}
