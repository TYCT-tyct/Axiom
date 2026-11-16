package operationsapi

import (
	"context"
	"testing"
)

func TestEmbeddedContract(t *testing.T) {
	doc, err := GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
}
