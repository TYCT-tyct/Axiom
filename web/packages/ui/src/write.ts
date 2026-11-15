import { ApiError } from "@axiom/api";
import { useRef, useState } from "react";

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
  // two quick changes both land and neither is sent on a stale copy.
  change: (edit: (current: T) => T) => void;
  saving: boolean;
  // saved is the last write succeeding, until the next change.
  saved: boolean;
  // failure is why a write failed: it and the edits queued behind it are dropped, so value is the server's again.
  failure: unknown;
  // stale is a 409 stale_revision: someone changed the object meanwhile, so it was read again and the edits made on
  // the old copy were not sent.
  stale: boolean;
};

type Outcome = { saved?: boolean; failure?: unknown; stale?: boolean };

export function useLiveSave<T>({
  initial,
  write,
  reread,
}: {
  initial: T;
  // write sends the object with the edit applied (the whole body, or the one value an endpoint takes) and returns
  // the object as the server now has it.
  write: (next: T) => Promise<T>;
  // reread fetches the object again; given only for a body with a version, whose 409 stale_revision it answers.
  reread?: () => Promise<T>;
}): LiveSave<T> {
  const confirmed = useRef(initial);
  const queue = useRef<((current: T) => T)[]>([]);
  const running = useRef(false);
  const [view, setView] = useState({
    value: initial,
    saving: false,
    saved: false,
    failure: null as unknown,
    stale: false,
  });

  const publish = (outcome: Outcome) =>
    setView({
      value: queue.current.reduce((value, edit) => edit(value), confirmed.current),
      saving: queue.current.length > 0,
      saved: false,
      failure: null,
      stale: false,
      ...outcome,
    });

  const recover = async (error: unknown): Promise<Outcome> => {
    if (!reread || !(error instanceof ApiError) || error.code !== "stale_revision") {
      return { failure: error };
    }
    try {
      confirmed.current = await reread();
      return { stale: true };
    } catch (failure) {
      return { failure };
    }
  };

  const drain = async () => {
    running.current = true;
    for (let edit = queue.current[0]; edit; edit = queue.current[0]) {
      try {
        confirmed.current = await write(edit(confirmed.current));
        queue.current.shift();
        publish({ saved: queue.current.length === 0 });
      } catch (error) {
        queue.current = [];
        publish(await recover(error));
      }
    }
    running.current = false;
  };

  return {
    mode: "live",
    ...view,
    change: (edit) => {
      queue.current.push(edit);
      publish({});
      if (!running.current) {
        void drain();
      }
    },
  };
}
