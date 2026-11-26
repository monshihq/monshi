package monshi_test

import (
	"context"
	"strings"
	"testing"

	"github.com/monshihq/monshi"
	"github.com/monshihq/monshi/internal/testfixture"
)

// Exercise the registration example through the same Load, Register and Create
// calls a host uses.
func TestRegistrationExampleLoads(t *testing.T) {
	d, err := monshi.Load("example", strings.NewReader(registrationExample(t)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if d.ID() != "example" {
		t.Errorf("ID = %q, want example", d.ID())
	}
	if w := d.Warnings(); len(w) != 0 {
		t.Fatalf("the registration example must load without warnings, got %v", w)
	}
	rt := monshi.NewRuntime()
	if err := rt.Register(d); err != nil {
		t.Fatal(err)
	}
	c, err := rt.Create(context.Background(), d.ID(), monshi.Actor{})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Definition(); got != d {
		t.Fatal("case did not resolve its registered definition")
	}
}

var registrationExample = testfixture.Example
