package routing_test

import (
	"testing"

	"axiom/internal/routing"
)

// TestClassSet covers the two meanings a preset's class list has: "*" also allows a class added after this node was
// built, while an explicit list fails closed on one it does not name.
func TestClassSet(t *testing.T) {
	t.Parallel()
	pool := routing.Class("pool")
	if !routing.AllClasses.Has(pool) {
		t.Fatal("* does not allow a class added later")
	}
	if !routing.AllClasses.Has(routing.ClassOfficial) {
		t.Fatal("* does not allow a seed")
	}
	if (routing.ClassSet{routing.ClassOfficial}).Has(pool) {
		t.Fatal("an explicit list allowed a class it does not name")
	}
	if !(routing.ClassSet{routing.ClassOfficial}).Has(routing.ClassOfficial) {
		t.Fatal("an explicit list did not allow the class it names")
	}
	if !(routing.ClassSet{}).Empty() {
		t.Fatal("the zero set is not empty")
	}
	if (routing.ClassSet{pool}).Empty() {
		t.Fatal("a set with a class is empty")
	}
}
