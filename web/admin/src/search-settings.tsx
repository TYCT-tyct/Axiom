import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { TextField } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "./change";
import { Facts, State } from "./parts";
import { searchSettingsQuery } from "./queries";
import { SettingsPage } from "./settings-page";

type Engine = AdminSchemas["SearchEngineInput"];
type ShownEngine = AdminSchemas["SearchEngine"];
type Settings = AdminSchemas["SearchSettings"];

const optional = (text: string): string | undefined => (text.trim() === "" ? undefined : text.trim());

// enginesOf reads a node that has engines, or one built before them: the old response has url and has_key only, and
// the page must keep working against it while the deployments are rolled one at a time.
function enginesOf(view: Settings): ShownEngine[] {
	if (Array.isArray(view.engines)) return view.engines;
	if (!view.url) return [];
	return [{
		id: "00000000-0000-0000-0000-000000000000",
		name: "searxng",
		kind: "searxng",
		url: view.url,
		has_key: view.has_key ?? false,
		enabled: true,
		default: true,
		cost_per_search: "0",
		cost_per_extra_result: "0",
		markup: "0",
		price_per_search: "0",
		price_per_extra_result: "0",
		price_version: 1,
	}];
}

// readEngines turns the indexed fields into the list the API takes. A row whose name is empty is a row the person did
// not fill in, not an engine without a name; the server checks the rest.
function readEngines(values: Record<string, string>): Engine[] | Problem {
	const engines: Engine[] = [];
	for (let i = 0; values[`name.${i}`] !== undefined; i++) {
		const name = (values[`name.${i}`] ?? "").trim();
		if (name === "") {
			continue;
		}
		engines.push({
			id: optional(values[`id.${i}`] ?? ""),
			name,
			kind: (values[`kind.${i}`] ?? "searxng") as Engine["kind"],
			url: optional(values[`url.${i}`] ?? ""),
			key: optional(values[`key.${i}`] ?? ""),
			enabled: values[`enabled.${i}`] === "on",
			default: values[`default.${i}`] === "on",
			cost_per_search: (values[`cost.${i}`] ?? "0").trim(),
			cost_per_extra_result: (values[`result.${i}`] ?? "0").trim(),
			markup: (values[`markup.${i}`] ?? "0").trim(),
		});
	}
	return engines;
}

export function SearchSettings() {
	const query = useQuery(searchSettingsQuery);
	const { t } = useTranslation();
	return (
		<SettingsPage
			query={query}
			labels={{
				title: t("settings.search.title"),
				unavailableLabel: t("settings.search.unavailableLabel"),
				unavailable: t("settings.search.unavailable"),
				edit: t("settings.search.edit"),
				test: t("settings.search.test"),
			}}
			facts={SearchFacts}
			dialogs={{ edit: SearchEditDialog, test: SearchTestDialog }}
		/>
	);
}

function SearchFacts({ view, action }: { view: Settings; action: ReactNode }) {
	const { t } = useTranslation();
	const source = view.source ?? "none";
	const engines = enginesOf(view);
	const rows: [string, ReactNode][] = [
		[
			t("settings.search.state"),
			<State key="state" tone={engines.length > 0 ? "good" : "neutral"}>
				{t(engines.length > 0 ? "settings.search.on" : "settings.search.off")}
			</State>,
		],
		[t("settings.search.source"), t(`settings.search.sources.${source}`)],
	];
	for (const engine of engines) {
		rows.push([
			`${engine.name} · ${engine.kind}`,
			<span key={engine.id}>
				{engine.enabled ? t("settings.search.enabled") : t("settings.search.disabled")}
				{engine.default ? ` · ${t("settings.search.default")}` : ""} ·{" "}
				{t("settings.search.price", {
					search: engine.price_per_search,
					result: engine.price_per_extra_result,
				})}
			</span>,
		]);
	}
	if (engines.length === 0) {
		rows.push([t("settings.search.engines"), t("settings.search.none")]);
	}
	if (view.restart_required) {
		rows.push([t("settings.search.restart"), t("settings.search.restartBody")]);
	}
	return <Facts title={t("settings.search.title")} rows={rows} action={action} />;
}

function SearchEditDialog({ view, onClose }: { view: Settings; onClose: () => void }) {
	const { t } = useTranslation();
	// An old node exposes only its single backend's URL and key. Do not show fields its contract cannot save: a
	// successful response would otherwise leave the extra engines, type, costs and switches unchanged.
	const legacy = !Array.isArray(view.engines);
	const rows = [...enginesOf(view), undefined];
	return (
		<ChangeDialog
			operation="search-settings"
			title={t("settings.search.edit")}
			intro={
				<>
					<p>{t("settings.search.intro")}</p>
					{legacy ? <p>{t("settings.search.legacyOnly")}</p> : null}
				</>
			}
			action={t("settings.search.save")}
			fields={(error) => legacy ? <LegacySearchFields view={view} error={error} /> : <SearchFields rows={rows} error={error} />}
			read={(values) => legacy ? readLegacyEngine(values, view) : readEngines(values)}
			send={(engines, { reason, key }) => {
				const params = { header: { "Idempotency-Key": key } };
				if (legacy) {
					return unwrapChange(adminApi.PUT("/api/admin/v1/settings/search", {
						params,
						body: { settings: { url: engines[0]?.url ?? "", key: engines[0]?.key }, reason },
					}));
				}
				return unwrapChange(adminApi.PUT("/api/admin/v1/settings/search", { params, body: { engines, reason } }));
			}}
			done={(saved) => <SearchSaved saved={saved} />}
			onClose={onClose}
		/>
	);
}

// readLegacyEngine is the old contract's one backend: URL and key only. The identity fields are hidden from the old
// form's fields but keep the response shape; the old node ignores them in its legacy PUT.
function readLegacyEngine(values: Record<string, string>, view: Settings): Engine[] {
	const saved = enginesOf(view)[0];
	return [{
		id: saved?.id,
		name: saved?.name ?? "searxng",
		kind: saved?.kind ?? "searxng",
		url: optional(values.url ?? ""),
		key: optional(values.key ?? ""),
		enabled: true,
		default: true,
		cost_per_search: saved?.cost_per_search ?? "0",
		cost_per_extra_result: saved?.cost_per_extra_result ?? "0",
		markup: saved?.markup ?? "0",
	}];
}

function LegacySearchFields({ view, error }: { view: Settings; error: unknown }) {
	const { t } = useTranslation();
	const errors = useErrors();
	const saved = enginesOf(view)[0];
	return (
		<div className="flex flex-col gap-4">
			<TextField
				label={t("settings.search.url")}
				hint={t("settings.search.urlHint")}
				name="url"
				inputMode="url"
				autoComplete="off"
				defaultValue={saved?.url ?? ""}
				error={errors.field(error, "settings/url")}
			/>
			<TextField
				label={t("settings.search.key")}
				hint={t("settings.search.keyHint")}
				name="key"
				type="password"
				autoComplete="new-password"
				error={errors.field(error, "settings")}
			/>
		</div>
	);
}

function SearchFields({ rows, error }: { rows: (ShownEngine | undefined)[]; error: unknown }) {
	const { t } = useTranslation();
	const errors = useErrors();
	return (
		<div className="flex flex-col gap-6">
			{rows.map((engine, i) => (
				<fieldset key={engine?.id ?? `new-${i}`} className="flex flex-col gap-3 border-t pt-4">
					<legend className="text-sm font-medium">
						{engine?.name || t("settings.search.newEngine")}
					</legend>
					{engine ? <input type="hidden" name={`id.${i}`} value={engine.id} /> : null}
					<div className="grid grid-cols-2 gap-3">
						<TextField
							label={t("settings.search.name")}
							hint={t("settings.search.nameHint")}
							name={`name.${i}`}
							autoComplete="off"
							defaultValue={engine?.name ?? ""}
							error={errors.field(error, "settings/engines")}
						/>
						<label className="flex flex-col gap-1 text-sm">
							<span>{t("settings.search.kind")}</span>
							<select name={`kind.${i}`} defaultValue={engine?.kind ?? "searxng"} className="rounded border p-2">
								<option value="searxng">SearXNG</option>
								<option value="firecrawl">Firecrawl</option>
							</select>
						</label>
					</div>
					<TextField
						label={t("settings.search.url")}
						hint={t("settings.search.urlHint")}
						name={`url.${i}`}
						inputMode="url"
						autoComplete="off"
						defaultValue={engine?.url ?? ""}
						error={errors.field(error, "settings/engines/url")}
					/>
					<TextField
						label={t("settings.search.key")}
						hint={t("settings.search.keyHint")}
						name={`key.${i}`}
						type="password"
						autoComplete="new-password"
						error={errors.field(error, "settings")}
					/>
					<div className="grid grid-cols-3 gap-3">
						<TextField
							label={t("settings.search.cost")}
							name={`cost.${i}`}
							inputMode="decimal"
							defaultValue={engine?.cost_per_search ?? "0"}
						/>
						<TextField
							label={t("settings.search.resultCost")}
							name={`result.${i}`}
							inputMode="decimal"
							defaultValue={engine?.cost_per_extra_result ?? "0"}
						/>
						<TextField
							label={t("settings.search.markup")}
							hint={t("settings.search.markupHint")}
							name={`markup.${i}`}
							inputMode="decimal"
							defaultValue={engine?.markup ?? "0"}
						/>
					</div>
					<div className="flex gap-6 text-sm">
						<label className="flex items-center gap-2">
							<input type="checkbox" name={`enabled.${i}`} defaultChecked={engine?.enabled ?? true} />
							{t("settings.search.enabled")}
						</label>
						<label className="flex items-center gap-2">
							<input type="checkbox" name={`default.${i}`} defaultChecked={engine?.default ?? false} />
							{t("settings.search.default")}
						</label>
					</div>
					{engine ? (
						<p className="text-xs opacity-70">
							{t("settings.search.published", {
								search: engine.price_per_search,
								result: engine.price_per_extra_result,
							})}
						</p>
					) : null}
				</fieldset>
			))}
		</div>
	);
}

function SearchSaved({ saved }: { saved: Settings }) {
	const { t } = useTranslation();
	return (
		<>
			<p>{t("settings.search.saved")}</p>
			{saved.restart_required === true ? <p>{t("settings.search.restartSaved")}</p> : null}
		</>
	);
}

function SearchTestDialog({ view, onClose }: { view: Settings; onClose: () => void }) {
	const { t } = useTranslation();
	const legacy = !Array.isArray(view.engines);
	const engine = enginesOf(view).find((e) => e.default) ?? enginesOf(view)[0];
	if (!engine) {
		return (
			<ChangeDialog
				operation="search-settings-test"
				title={t("settings.search.testTitle")}
				intro={<p>{t("settings.search.testNone")}</p>}
				action={t("settings.search.send")}
				verdict
				fields={() => null}
				read={() => ({})}
				send={async () => ({ data: { status: "disabled" as const }, replayed: false })}
				done={(result) => <p>{t(`settings.search.status.${result.status ?? "failed"}`)}</p>}
				onClose={onClose}
			/>
		);
	}
	const input: Engine = {
		id: engine.id,
		name: engine.name,
		kind: engine.kind,
		url: engine.url,
		enabled: engine.enabled,
		default: engine.default,
		cost_per_search: engine.cost_per_search,
		cost_per_extra_result: engine.cost_per_extra_result,
		markup: engine.markup,
	};
	return (
		<ChangeDialog
			operation="search-settings-test"
			title={t("settings.search.testTitle")}
			intro={<p>{t("settings.search.testIntro", { name: engine.name })}</p>}
			action={t("settings.search.send")}
			verdict
			fields={() => null}
			read={() => ({})}
			send={(_, { reason, key }) =>
				unwrapChange(
					adminApi.POST("/api/admin/v1/settings/search/tests", {
						params: { header: { "Idempotency-Key": key } },
						body: legacy ? { reason } : { engine: input, reason },
					}),
				)
			}
			done={(result) => <p>{t(`settings.search.status.${result.status ?? "failed"}`)}</p>}
			onClose={onClose}
		/>
	);
}
