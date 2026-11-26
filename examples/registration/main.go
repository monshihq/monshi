package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/monshihq/monshi"
)

const definition = `
roles {
    Applicant { allow view, edit, submit registration; }
}
forms {
    registration { name : string required; }
}
stages {
    draft { allow Applicant accept; accept filed; }
    filed { require registration; }
}
`

func main() {
	ctx := context.Background()
	rt := monshi.NewRuntime()
	d, err := rt.Define("registration", strings.NewReader(definition))
	if err != nil {
		log.Fatal(err)
	}
	c, err := rt.Create(ctx, d.ID(), monshi.Actor{User: "ada", PrimaryRole: "Applicant"})
	if err != nil {
		log.Fatal(err)
	}

	// A new case starts at revision zero, with an empty working copy.
	first, err := c.Transition(ctx, "filed", 0)
	if !monshi.Failed(err, monshi.InvalidOp) || len(first.Missing) == 0 {
		log.Fatalf("expected filing to report a missing field, got %v", err)
	}
	fmt.Println("Cannot file: missing", strings.Join(first.Missing, ", "))

	edit, err := c.Update(ctx, 0, monshi.Set("registration.name", monshi.Text("Ada")))
	if err != nil {
		log.Fatal(err)
	}
	submitted, err := c.Submit(ctx, "registration", edit.Working)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Submitted registration at revision", submitted.Revision)

	filed, err := c.Transition(ctx, "filed", submitted.Revision)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Stage: %s (revision %d)\n", filed.Stage, filed.Revision)
}
