// What a new key's owner pastes into a client: an example of each protocol the gateway speaks, and the settings of the
// clients the docs name. Every address here is built from the site's API domain (`Site.api_base_url`) and never from
// this page's own address: the console runs on the site's public domain, while calls go to the API domain it registered
// (docs/domains/accounts.md「API key」).

export type GuideProtocol = "chat" | "responses" | "messages";
export type GuideClient = "claude-code" | "codex" | "cursor";
type GuideEntry<Id extends string> = { id: Id; text: string };
type KeyGuide = { protocols: GuideEntry<GuideProtocol>[]; clients: GuideEntry<GuideClient>[] };

// Which models the site serves is not known here, so the examples leave the name to fill in: `GET /v1/models` lists the
// ones this key may call.
const model = "<model>";
const hello = [{ role: "user", content: "Hello" }];

// An OpenAI-shaped client takes a base that already ends in /v1 and appends the endpoint, while Anthropic's client takes
// the host and appends /v1/messages itself (internal/protocol/internal/record/main.go).
const openaiBase = (apiBase: string) => `${apiBase}/v1`;

const curl = (url: string, headers: string[], body: object) =>
  [`curl ${url} \\`, ...headers.map((header) => `  -H "${header}" \\`), `  -d '${JSON.stringify(body)}'`].join("\n");

const json = "Content-Type: application/json";

function protocolEntries(apiBase: string, secret: string): GuideEntry<GuideProtocol>[] {
  const base = openaiBase(apiBase);
  const bearer = `Authorization: Bearer ${secret}`;
  // Anthropic's own header and version, which the gateway accepts as readily as a bearer token.
  const anthropic = [`x-api-key: ${secret}`, "anthropic-version: 2023-06-01", json];
  return [
    { id: "chat", text: curl(`${base}/chat/completions`, [bearer, json], { model, messages: hello }) },
    { id: "responses", text: curl(`${base}/responses`, [bearer, json], { model, input: "Hello" }) },
    { id: "messages", text: curl(`${base}/messages`, anthropic, { model, max_tokens: 1024, messages: hello }) },
  ];
}

function clientEntries(apiBase: string, secret: string): GuideEntry<GuideClient>[] {
  return [
    {
      // ANTHROPIC_AUTH_TOKEN is sent as a bearer token, and discovery is what puts the site's models in /model, since
      // their names are not Claude Code's built-in ones (code.claude.com/docs/en/llm-gateway-connect).
      id: "claude-code",
      text: [
        `export ANTHROPIC_BASE_URL=${apiBase}`,
        `export ANTHROPIC_AUTH_TOKEN=${secret}`,
        "export CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1",
      ].join("\n"),
    },
    {
      // env_key names the variable Codex reads the key from, so the key itself stays out of the file; wire_api is what
      // sends Codex to the Responses endpoint (developers.openai.com/codex/config-reference).
      id: "codex",
      text: [
        'model_provider = "axiom"',
        `model = "${model}"`,
        "",
        "[model_providers.axiom]",
        'name = "Axiom"',
        `base_url = "${openaiBase(apiBase)}"`,
        'env_key = "AXIOM_API_KEY"',
        'wire_api = "responses"',
      ].join("\n"),
    },
    // Cursor has no config file: this address goes into its OpenAI base URL override, and the key into its key field.
    { id: "cursor", text: openaiBase(apiBase) },
  ];
}

// A site with no API domain has nowhere to point a client, so there is no guide at all.
export function keyGuide(apiBase: string, secret: string): KeyGuide | undefined {
  if (apiBase === "") {
    return undefined;
  }
  return { protocols: protocolEntries(apiBase, secret), clients: clientEntries(apiBase, secret) };
}
