package payments

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"axiom/internal/platform/adminapi"
)

// TestSettingsKeepTheirContract: the admin contract names each kind's settings members (Merchant.settings), and each
// plugin reads exactly the members of its settings struct, refusing any other, so the two lists must be the same.
func TestSettingsKeepTheirContract(t *testing.T) {
	t.Parallel()
	doc, err := adminapi.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind     Kind
		schema   string
		settings any
	}{
		{Alipay{}.Kind(), "AlipaySettings", alipaySettings{}},
		{EasyPay{}.Kind(), "EasyPaySettings", easyPaySettings{}},
		{WechatPay{}.Kind(), "WechatPaySettings", wechatSettings{}},
		{Stripe{}.Kind(), "StripeSettings", struct{}{}},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			t.Parallel()
			typ := reflect.TypeOf(tc.settings)
			read := make([]string, 0, typ.NumField())
			for field := range typ.Fields() {
				name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
				read = append(read, name)
			}
			slices.Sort(read)
			named := slices.Sorted(maps.Keys(doc.Components.Schemas[tc.schema].Value.Properties))
			if !slices.Equal(read, named) {
				t.Errorf("the plugin reads %v, the contract's %s names %v", read, tc.schema, named)
			}
		})
	}
}
