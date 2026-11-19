import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { accountKey, can, LoadMore, useErrors, useSession } from "@axiom/app";
import Keys from "@axiom/boards/AxApp/regions/Keys";
import KeysEN from "@axiom/boards/AxApp/regions/Keys.en";
import KeysZH from "@axiom/boards/AxApp/regions/Keys.zh";
import {
  Button,
  DataState,
  EmptyState,
  FormError,
  Icon,
  Note,
  Notice,
  PageFrame,
  useFormat,
  usePageTitle,
} from "@axiom/ui";
import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, useSearch } from "@tanstack/react-router";
import { type MouseEvent, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { pressBoardButton } from "../board-interaction";
import { CreateKeyDialog, DeleteKeyDialog, SecretDialog } from "../key-dialogs";
import { keyFiltersApplied } from "../key-filters";
import { type KeyFilters, keysQuery } from "../queries";
import { keyRow, today } from "./keys-data";
import { KeyDetailDialog } from "./keys-detail";
import { hasFilters, KeyFiltersDialog, noFilters } from "./keys-filters";

type Key = Schemas["Key"];
type Page = { data: Key[]; next_cursor?: string | null; filters?: Schemas["KeyFilterEcho"] };
type ListQuery = ReturnType<typeof useInfiniteQuery<Page>>;
type Dialog =
  | { kind: "create" }
  | { kind: "secret"; created: Schemas["CreatedKey"]; asked: string | undefined }
  | { kind: "filter" }
  | { kind: "detail"; key: Key }
  | { kind: "delete"; key: Key };
// What the notice offers to undo: which key was switched, and whether it was on before the save.
type Toggled = { id: string; name: string; was: boolean };

// The console's keys page is the board's Keys slice (#747 C1): the personal space's list, drawn from the API. The board
// draws the same slice for both its boards — the phone's markup is the desktop's, region for region — so one region
// serves both widths. A key opens in the console's dialog with its settings (see keys-detail.tsx); the create form and
// the key's one-time text are the console's too, because the board draws its versions of both in JavaScript, which the
// region converter does not read.
export default function KeysPage() {
  const { t, i18n } = useTranslation();
  const manage = can(useSession(), "api_keys.manage");
  const format = useFormat();
  // The address keeps parameters the page does not know, and the router hands them on with the ones it validated (x=1
  // from a link that carried it); only the key filters narrow the list.
  const search = useSearch({ from: "/app/keys" });
  const filters: KeyFilters = {
    name: search.name,
    prefix: search.prefix,
    status: search.status,
    created_after: search.created_after,
    created_before: search.created_before,
    workspace_id: search.workspace_id,
  };
  const keys = useInfiniteQuery(keysQuery(filters));
  const pages = keys.data?.pages ?? [];
  const list = useMemo(() => pages.flatMap((page) => page.data), [pages]);
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const [toggled, setToggled] = useState<Toggled | null>(null);
  usePageTitle(t("nav.keys"));
  const english = (i18n.resolvedLanguage ?? i18n.language).startsWith("en");
  const boardT = (key: keyof typeof KeysZH) => (english ? KeysEN[key] : KeysZH[key]);
  const now = Date.now();
  const rows = useMemo(
    () => list.map((key) => keyRow(key, { format, t, language: i18n.language, now, today: today(now) })),
    [list, format, t, i18n.language, now],
  );
  const close = () => setDialog(null);
  // A save that flipped the switch offers that switch back; any other save of the same key drops the offer, because the
  // list may not have caught it and what the list holds is not what the key now is.
  const saved = (key: Key, before: Key) =>
    setToggled((current) =>
      key.disabled !== before.disabled
        ? { id: key.id, name: key.name, was: before.disabled }
        : current?.id === key.id
          ? null
          : current,
    );
  // The notice stands only while the list shows the state the save left: a key deleted since, or one the list has not
  // caught up on, is not offered a switch it may not have.
  const switched = toggled ? list.find((key) => key.id === toggled.id) : undefined;
  const notice = switched && switched.disabled !== toggled?.was ? switched : undefined;
  return (
    <PageFrame>
      {notice && toggled ? (
        <ToggleNotice apiKey={notice} name={toggled.name} was={toggled.was} onDone={() => setToggled(null)} />
      ) : null}
      <Content
        keys={keys}
        list={list}
        rows={rows}
        pages={pages}
        filters={filters}
        manage={manage}
        boardT={boardT}
        setDialog={setDialog}
      />
      <Dialogs dialog={dialog} setDialog={setDialog} close={close} filters={filters} onSaved={saved} />
    </PageFrame>
  );
}

// Content is what the page shows of the account's keys: the board's list when it has any, and the state that replaces
// the list while it loads, when the account has none, and when the filters find none or a node that predates them.
function Content({
  keys,
  list,
  rows,
  pages,
  filters,
  manage,
  boardT,
  setDialog,
}: {
  keys: ListQuery;
  list: Key[];
  rows: ReturnType<typeof keyRow>[];
  pages: { filters?: Schemas["KeyFilterEcho"] }[];
  filters: KeyFilters;
  manage: boolean;
  boardT: (key: keyof typeof KeysZH) => string;
  setDialog: (dialog: Dialog | null) => void;
}) {
  const { t } = useTranslation();
  if (keys.isPending || keys.isError) {
    return (
      <DataState query={keys} page>
        {() => null}
      </DataState>
    );
  }
  // A node before key filters answers every key and no echo of what was asked: its rows are not the filtered result,
  // whether it sent none or many.
  if (hasFilters(filters) && !keyFiltersApplied(filters, pages)) {
    return <FiltersUnavailable manage={manage} onCreate={() => setDialog({ kind: "create" })} />;
  }
  if (list.length === 0) {
    return <EmptyList filtered={hasFilters(filters)} manage={manage} onCreate={() => setDialog({ kind: "create" })} />;
  }
  return (
    <>
      <Retired keys={list} />
      <Keys
        t={boardT}
        text={{ usedHeader: t("keys.board.usedHeader") }}
        values={rowValues(manage, list, (key) => setDialog({ kind: "detail", key }))}
        rows={{ keys0: rows }}
        actions={
          <Actions
            manage={manage}
            onFilter={() => setDialog({ kind: "filter" })}
            onCreate={() => setDialog({ kind: "create" })}
          />
        }
      />
      <LoadMore list={keys} />
    </>
  );
}

// Actions are the page's own, beside the board's title: the list has no filters of its own (L8), and an account that
// may change keys makes one here.
function Actions({ manage, onFilter, onCreate }: { manage: boolean; onFilter: () => void; onCreate: () => void }) {
  const { t } = useTranslation();
  return (
    <>
      <Button size="md" onClick={onFilter}>
        <Icon name="search" />
        {t("keys.board.filter")}
      </Button>
      {manage ? (
        <Button size="md" variant="primary" onClick={onCreate}>
          <Icon name="plus" />
          {t("keys.create")}
        </Button>
      ) : null}
    </>
  );
}

// What the region's rows need from the page. A row opens its key only for an account that may change keys; others read
// the list, and every row's icon says nothing to a reader either way. Pressing a row presses the click it stands for.
function rowValues(manage: boolean, list: Key[], choose: (key: Key) => void) {
  if (!manage) {
    return { kr2_0: "", rowTab: "-1", decorative: true };
  }
  return { kr2_0: "", rowTab: "0", decorative: true, press: pressBoardButton, ko2_0: openRow(list, choose) };
}

// The key a row stands for, from the row the event came from: the list's rows are the page's, in its order.
function openRow(list: Key[], choose: (key: Key) => void) {
  return (event: MouseEvent<Element>) => {
    const row = (event.currentTarget as Element).closest("tr");
    const body = row?.parentElement;
    const key = row && body ? list[[...body.children].indexOf(row)] : undefined;
    if (key) {
      choose(key);
    }
  };
}

// A model the catalog has retired cannot serve a key that names it, and the board's list has no column to say it in
// (L7): the names are here, once, above the list.
function Retired({ keys }: { keys: Key[] }) {
  const { t } = useTranslation();
  const retired = [...new Set(keys.flatMap((key) => key.retired_models ?? []))];
  if (retired.length === 0) {
    return null;
  }
  return <Note title={t("keys.retiredModels", { models: retired.join(", ") })} />;
}

// EmptyList says why the list is empty and what to do about it: no key yet, or no match for the filters.
function EmptyList({ filtered, manage, onCreate }: { filtered: boolean; manage: boolean; onCreate: () => void }) {
  const { t } = useTranslation();
  const actions = (
    <>
      <Button size="md" asChild>
        <Link to="/keys" search={noFilters}>
          {t("keys.filter.reset")}
        </Link>
      </Button>
      {manage ? (
        <Button size="md" variant="primary" onClick={onCreate}>
          {t("keys.create")}
        </Button>
      ) : null}
    </>
  );
  if (filtered) {
    return (
      <EmptyState page title={t("keys.noMatch")} action={actions}>
        {t("keys.noMatchBody")}
      </EmptyState>
    );
  }
  return (
    <EmptyState page title={t("keys.empty.title")} action={manage ? actions : undefined}>
      {t("keys.empty.body")}
    </EmptyState>
  );
}

// FiltersUnavailable says the node answered every key because it predates key filters, so the rows it sent are not the
// filtered list and are not shown.
function FiltersUnavailable({ manage, onCreate }: { manage: boolean; onCreate: () => void }) {
  const { t } = useTranslation();
  return (
    <EmptyState
      page
      title={t("keys.filtersUnavailable.title")}
      action={
        <>
          <Button size="md" asChild>
            <Link to="/keys" search={noFilters}>
              {t("keys.filter.reset")}
            </Link>
          </Button>
          {manage ? (
            <Button size="md" variant="primary" onClick={onCreate}>
              {t("keys.create")}
            </Button>
          ) : null}
        </>
      }
    >
      {t("keys.filtersUnavailable.body")}
    </EmptyState>
  );
}

// Dialogs holds the page's modals: the filters, the key that was just made and its one-time text, a key's settings, and
// the confirmation that deletes it.
function Dialogs({
  dialog,
  setDialog,
  close,
  filters,
  onSaved,
}: {
  dialog: Dialog | null;
  setDialog: (dialog: Dialog | null) => void;
  close: () => void;
  filters: KeyFilters;
  onSaved: (key: Key, before: Key) => void;
}) {
  if (dialog?.kind === "create") {
    return (
      <CreateKeyDialog
        workspace={filters.workspace_id}
        onClose={close}
        onCreated={(created, asked) => setDialog({ kind: "secret", created, asked })}
      />
    );
  }
  if (dialog?.kind === "secret") {
    return <SecretDialog created={dialog.created} asked={dialog.asked} onClose={close} />;
  }
  if (dialog?.kind === "filter") {
    return <KeyFiltersDialog filters={filters} onClose={close} />;
  }
  if (dialog?.kind === "detail") {
    return (
      <KeyDetailDialog
        apiKey={dialog.key}
        onClose={close}
        onDelete={(key) => setDialog({ kind: "delete", key })}
        onSaved={onSaved}
      />
    );
  }
  if (dialog?.kind === "delete") {
    return <DeleteKeyDialog apiKey={dialog.key} onClose={close} />;
  }
  return null;
}

// ToggleNotice offers a key the state it had before the last save. Undoing reads the key first and writes it back with
// the switch flipped, since the contract replaces every setting it is given: a name or a limit changed since stays
// changed, and a list that did not catch up cannot put older settings back. A failed read writes nothing. A failure says
// why, and the button stays for another try while the answer is on its way.
function ToggleNotice({ apiKey, name, was, onDone }: { apiKey: Key; name: string; was: boolean; onDone: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const queryClient = useQueryClient();
  const undo = useMutation({
    mutationFn: async () => {
      const latest = await unwrap(
        consoleApi.GET("/api/console/v1/keys/{key_id}", { params: { path: { key_id: apiKey.id } } }),
      );
      if (latest.disabled === was) {
        return latest;
      }
      return unwrap(
        consoleApi.PUT("/api/console/v1/keys/{key_id}", {
          params: { path: { key_id: apiKey.id } },
          body: {
            name: latest.name,
            disabled: was,
            spend_limit: latest.spend_limit,
            limit_reset: latest.limit_reset,
            models: latest.models,
            ip_allow: latest.ip_allow,
          },
        }),
      );
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: [accountKey, "keys"] });
      onDone();
    },
  });
  return (
    <Notice
      action={
        <Button size="sm" disabled={undo.isPending} onClick={() => undo.mutate()}>
          {t("keys.board.undo")}
        </Button>
      }
    >
      {t(apiKey.disabled ? "keys.board.disabledNotice" : "keys.board.enabledNotice", { name })}
      {undo.isError ? <FormError>{errors.message(undo.error)}</FormError> : null}
    </Notice>
  );
}
