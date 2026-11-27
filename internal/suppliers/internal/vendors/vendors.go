// Package vendors is the table of providers that speak OpenAI's formats at an address of their own and differ only in
// it, their paths and the settings their keys come with (docs/domains/suppliers-direct.md「兼容 OpenAI 的厂商表」). Each
// row is a channel type; deleting a row removes the type, and nothing outside this package names one. A provider with a
// protocol or signing of its own is a package of its own instead.
package vendors

import (
	"errors"

	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/direct"
)

// keyForm is the form of a provider whose credential is its keys.
var keyForm = []adapter.Form{direct.Keys}

// rows are the providers, one each. A row's Anthropic models are the ones its Anthropic-compatible API serves by the
// provider's documentation; "*" only where it says every model is served.
var rows = []direct.Provider{
	{
		Type: adapter.Type{
			Kind: "openai", Name: "OpenAI", Vendor: "openai", Serves: []string{"openai"}, Group: adapter.GroupOfficial,
			Entries:  []adapter.PriceEntry{{ID: "global", Origin: "https://api.openai.com", Currency: "USD"}},
			Families: []adapter.Family{adapter.FamilyOpenAI}, Forms: keyForm, Lists: true,
			// A legacy user key that belongs to several organizations bills the default one unless told; a project key
			// needs neither.
			Fields: []adapter.Field{
				{Name: "organization", Pattern: `^org-[A-Za-z0-9]{1,64}$`, Max: 68, Header: "OpenAI-Organization"},
				{Name: "project", Pattern: `^proj_[A-Za-z0-9]{1,64}$`, Max: 69, Header: "OpenAI-Project"},
			},
		},
		Responses: true,
	},
	{
		Type: adapter.Type{
			Kind: "google", Name: "Google Gemini", Vendor: "google", Serves: []string{"google"}, Group: adapter.GroupOfficial,
			Entries:  []adapter.PriceEntry{{ID: "global", Origin: "https://generativelanguage.googleapis.com", Currency: "USD"}},
			Families: []adapter.Family{adapter.FamilyOpenAI}, Forms: keyForm, Lists: true,
		},
		// Google serves the OpenAI formats under /v1beta/openai: the gateway does not send Gemini's own generateContent.
		Root: "/v1beta/openai",
	},
	{
		Type: adapter.Type{
			Kind: "moonshot", Name: "Kimi", Vendor: "moonshotai", Serves: []string{"moonshotai"}, Group: adapter.GroupDomestic,
			Entries: []adapter.PriceEntry{
				{ID: "cn", Origin: "https://api.moonshot.cn", Currency: "CNY"},
				{ID: "global", Origin: "https://api.moonshot.ai", Currency: "USD"},
			},
			Families: []adapter.Family{adapter.FamilyOpenAI, adapter.FamilyAnthropic}, Forms: keyForm, Lists: true,
		},
		Anthropic: &direct.Anthropic{Root: "/anthropic", Models: []string{"kimi-k3.1-pro", "kimi-k3.1-pro-highspeed", "kimi-k3"}},
		Balance:   &direct.Balance{Path: "/v1/users/me/balance", Read: moonshotBalance},
	},
	{
		Type: adapter.Type{
			Kind: "deepseek", Name: "DeepSeek", Vendor: "deepseek", Serves: []string{"deepseek"}, Group: adapter.GroupDomestic,
			// One address, two price lists: the yuan prices of the Chinese page and the dollar prices of the English one.
			Entries: []adapter.PriceEntry{
				{ID: "cny", Origin: "https://api.deepseek.com", Currency: "CNY"},
				{ID: "usd", Origin: "https://api.deepseek.com", Currency: "USD"},
			},
			Families: []adapter.Family{adapter.FamilyOpenAI, adapter.FamilyAnthropic}, Forms: keyForm, Lists: true,
		},
		Anthropic: &direct.Anthropic{Root: "/anthropic", Models: []string{"deepseek-flash", "deepseek-v4-pro"}},
		Balance:   &direct.Balance{Path: "/user/balance", Read: deepseekBalance},
	},
	{
		Type: adapter.Type{
			Kind: "ark", Name: "火山方舟", Vendor: "bytedance-seed", Serves: []string{"bytedance-seed"}, Group: adapter.GroupDomestic,
			Entries:  []adapter.PriceEntry{{ID: "cn", Origin: "https://ark.cn-beijing.volces.com", Currency: "CNY"}},
			Families: []adapter.Family{adapter.FamilyOpenAI}, Forms: keyForm, Lists: true,
		},
		Root: "/api/v3",
	},
	{
		Type: adapter.Type{
			Kind: "qianfan", Name: "百度千帆", Vendor: "baidu", Serves: []string{"baidu"}, Group: adapter.GroupDomestic,
			Entries:  []adapter.PriceEntry{{ID: "cn", Origin: "https://qianfan.baidubce.com", Currency: "CNY"}},
			Families: []adapter.Family{adapter.FamilyOpenAI}, Forms: keyForm, Lists: true,
		},
		Root: "/v2",
	},
	{
		Type: adapter.Type{
			Kind: "minimax", Name: "MiniMax", Vendor: "minimax", Serves: []string{"minimax"}, Group: adapter.GroupDomestic,
			Entries: []adapter.PriceEntry{
				{ID: "cn", Origin: "https://api.minimax.cn", Currency: "CNY"},
				{ID: "global", Origin: "https://api.minimax.io", Currency: "USD"},
			},
			Families: []adapter.Family{adapter.FamilyOpenAI, adapter.FamilyAnthropic}, Forms: keyForm, Lists: true,
		},
		Anthropic: &direct.Anthropic{Root: "/anthropic", Models: []string{"MiniMax-M3.1-Flash", "MiniMax-M3"}},
	},
	{
		Type: adapter.Type{
			Kind: "zhipu", Name: "智谱", Vendor: "z-ai", Serves: []string{"z-ai"}, Group: adapter.GroupDomestic,
			Entries: []adapter.PriceEntry{
				{ID: "cn", Origin: "https://open.bigmodel.cn", Currency: "CNY"},
				{ID: "global", Origin: "https://api.z.ai", Currency: "USD"},
			},
			Families: []adapter.Family{adapter.FamilyOpenAI, adapter.FamilyAnthropic}, Forms: keyForm, Lists: true,
		},
		// The list at the root's /models is not in Zhipu's reference, but it answers a key that is not valid with a
		// 401, as the check needs (probed 2026-11-27 at both origins).
		Root:      "/api/paas/v4",
		Anthropic: &direct.Anthropic{Root: "/api/anthropic", Models: []string{"glm-5.3", "glm-5.3-flash", "glm-5.2"}},
	},
	{
		Type: adapter.Type{
			Kind: "bailian", Name: "阿里云百炼", Vendor: "qwen", Serves: []string{"qwen"}, Group: adapter.GroupDomestic,
			Entries: []adapter.PriceEntry{
				{ID: "cn", Origin: "https://dashscope.aliyuncs.com", Currency: "CNY"},
				{ID: "global", Origin: "https://dashscope-intl.aliyuncs.com", Currency: "USD"},
			},
			Families: []adapter.Family{adapter.FamilyOpenAI, adapter.FamilyAnthropic}, Forms: keyForm, Lists: true,
		},
		Root: "/compatible-mode/v1",
		// Bailian also sells other authors' models (DeepSeek, Kimi, GLM) at prices of its own; they are listed and stay
		// unpriced, since the type serves Qwen alone.
		Anthropic: &direct.Anthropic{Root: "/apps/anthropic", Models: []string{
			"qwen4-max", "qwen3.8-max", "qwen3.7-max", "qwen3.7-plus", "qwen3.6-plus", "qwen3.5-plus", "qwen-plus", "qwen-plus-latest",
			"qwen3.8-flash", "qwen3.7-flash", "qwen3.6-flash", "qwen3.5-flash", "qwen-flash", "qwen3-coder-flash", "qwen3-vl-plus",
		}},
	},
	{
		Type: adapter.Type{
			Kind: "xai", Name: "xAI", Vendor: "x-ai", Serves: []string{"x-ai"}, Group: adapter.GroupOfficial,
			Entries:  []adapter.PriceEntry{{ID: "global", Origin: "https://api.x.ai", Currency: "USD"}},
			Families: []adapter.Family{adapter.FamilyOpenAI}, Forms: keyForm, Lists: true,
		},
		Responses: true,
	},
	{
		Type: adapter.Type{
			Kind: "mistral", Name: "Mistral", Vendor: "mistralai", Serves: []string{"mistralai"}, Group: adapter.GroupOfficial,
			Entries:  []adapter.PriceEntry{{ID: "global", Origin: "https://api.mistral.ai", Currency: "USD"}},
			Families: []adapter.Family{adapter.FamilyOpenAI}, Forms: keyForm, Lists: true,
		},
	},
}

// deepseekBalance reads DeepSeek's {"balance_infos":[{"currency","total_balance"}]}, one entry a currency the account
// holds: the price entry's when it holds that one, else the first.
func deepseekBalance(body []byte, currency platform.Currency) (adapter.Money, error) {
	var b struct {
		Infos []struct {
			Currency string `json:"currency"`
			Total    string `json:"total_balance"`
		} `json:"balance_infos"`
	}
	if err := jsonx.Unmarshal(body, &b); err != nil {
		return adapter.Money{}, err
	}
	if len(b.Infos) == 0 {
		return adapter.Money{}, errors.New("no balance in the answer")
	}
	info := b.Infos[0]
	for _, i := range b.Infos {
		if i.Currency == currency.String() {
			info = i
		}
	}
	return adapter.ParseMoney(info.Total, info.Currency)
}

// moonshotBalance reads Kimi's {"data":{"available_balance":…}}, a JSON number in the price entry's currency, which the
// answer does not name. The number's text is read as it is, so no digit is lost to a float.
func moonshotBalance(body []byte, currency platform.Currency) (adapter.Money, error) {
	var b struct {
		Data struct {
			Available jsonx.Raw `json:"available_balance"`
		} `json:"data"`
	}
	if err := jsonx.Unmarshal(body, &b); err != nil {
		return adapter.Money{}, err
	}
	return adapter.ParseMoney(string(b.Data.Available), currency.String())
}

// New is an adapter for each row.
func New() []adapter.Adapter {
	out := make([]adapter.Adapter, len(rows))
	for i, p := range rows {
		out[i] = direct.New(p)
	}
	return out
}
