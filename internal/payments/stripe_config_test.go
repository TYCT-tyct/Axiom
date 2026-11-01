package payments

import (
	"testing"

	"axiom/internal/platform"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/secret"
)

func TestStripeOpenValidatesConfiguration(t *testing.T) {
	base := func() MerchantConfig {
		return MerchantConfig{
			Currency: currency(t, "USD"), Settings: []byte("{}"), Client: new(egress.Client),
			Secrets: map[string]secret.Secret{
				"secret_key": secret.New("sk_test_123"), "webhook_secret": secret.New("whsec_current"),
			},
		}
	}
	if _, err := (Stripe{}).Open(base()); err != nil {
		t.Fatalf("valid configuration: %v", err)
	}
	valid := base()
	valid.Secrets["old_webhook_secret"] = secret.New("whsec_old")
	if _, err := (Stripe{}).Open(valid); err != nil {
		t.Fatalf("rotation configuration: %v", err)
	}
	for name, change := range map[string]func(*MerchantConfig){
		"missing key":      func(c *MerchantConfig) { delete(c.Secrets, "secret_key") },
		"missing webhook":  func(c *MerchantConfig) { delete(c.Secrets, "webhook_secret") },
		"bad key":          func(c *MerchantConfig) { c.Secrets["secret_key"] = secret.New("pk_test_123") },
		"bad webhook":      func(c *MerchantConfig) { c.Secrets["webhook_secret"] = secret.New("nope") },
		"bad old webhook":  func(c *MerchantConfig) { c.Secrets["old_webhook_secret"] = secret.New("nope") },
		"unknown secret":   func(c *MerchantConfig) { c.Secrets["extra"] = secret.New("x") },
		"unknown setting":  func(c *MerchantConfig) { c.Settings = []byte(`{"price":"price_1"}`) },
		"missing currency": func(c *MerchantConfig) { c.Currency = platform.Currency{} },
		"missing client":   func(c *MerchantConfig) { c.Client = nil },
	} {
		t.Run(name, func(t *testing.T) {
			c := base()
			change(&c)
			if _, err := (Stripe{}).Open(c); err == nil {
				t.Fatal("accepted invalid configuration")
			}
		})
	}
}

func TestStripeValidateMerchantChecksEnabledUpstreams(t *testing.T) {
	ok := MerchantInput{Methods: []MethodInput{{Upstream: "card", Enabled: true}, {Upstream: "paypal", Enabled: false}}}
	if err := (Stripe{}).validateMerchant(ok); err != nil {
		t.Fatalf("disabled unknown method: %v", err)
	}
	for _, upstream := range []string{"card", "alipay", "wechat_pay", "link"} {
		if err := (Stripe{}).validateMerchant(MerchantInput{Methods: []MethodInput{{Upstream: upstream, Enabled: true}}}); err != nil {
			t.Fatalf("%s: %v", upstream, err)
		}
	}
	if err := (Stripe{}).validateMerchant(MerchantInput{Methods: []MethodInput{{Upstream: "paypal", Enabled: true}}}); err == nil {
		t.Fatal("accepted an enabled unsupported method")
	}
}
