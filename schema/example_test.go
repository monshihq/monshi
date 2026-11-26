package schema_test

import (
	"fmt"
	"github.com/monshihq/monshi/internal/testfixture"
	"strings"
	"testing"

	"github.com/monshihq/monshi/schema"
)

func ExampleParse() {
	const src = `
roles {
    Manager {
        allow view report;
    }
}

forms {
    report verifiable {
        title : string required;
    }
}

stages {
    draft {
        allow Manager view, edit, submit report;
        allow Manager accept, reject;
        accept done;
    }
    done {
        require report;
    }
}
`
	s, err := schema.Parse(strings.NewReader(src))
	if err != nil {
		fmt.Println("I/O failure:", err)
		return
	}
	fmt.Println("errors:", len(s.Errors))
	fmt.Println("warnings:", len(s.Warnings))
	fmt.Println("initial stage:", s.Initial.Name)
	fmt.Println("report.verifiable:", s.Forms[0].Verifiable)
	fmt.Println("title.required:", s.Forms[0].Nodes[0].Required)
	fmt.Println("done.requires report:", s.Stages[1].Requires[0] == s.Forms[0])

	// Output:
	// errors: 0
	// warnings: 0
	// initial stage: draft
	// report.verifiable: true
	// title.required: true
	// done.requires report: true
}

// FuzzParse checks that arbitrary input terminates without a panic and that the
// first declared stage, if present, is initial.
func FuzzParse(f *testing.F) {
	seeds := []string{
		minimalSrc,
		testfixture.Example(f),
		"",
		"types { }",
		"???",
		`forms { f { a : string default "oops } }`,
		`forms { f { a : string; } } stages { d { } } /* dangling`,
		"forms {\n\tf {\n\t\tk : enum(A, B) required;\n\t\ts : signature(K);\n\t}\n}\nstages {\n\td { accept[1 == 1] e; }\n\te { }\n}",
		`stages { d { } } forms { f { } }`,
		`roles { R { allow view, accept f; } } forms { f { } } stages { d { } }`,
		`forms { f { a : decimal(4, 2) default 1.20; b : string present[.a == 1.20]; } } stages { d { } }`,
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, src string) {
		s, err := schema.Parse(strings.NewReader(src))
		switch {
		case err != nil && s != nil:
			t.Fatalf("Go error %v with non-nil schema", err)
		case err == nil && s == nil:
			t.Fatal("nil schema without Go error")
		}
		if len(s.Stages) > 0 && s.Initial != s.Stages[0] {
			t.Fatal("Initial must be the first declared stage")
		}
		if len(s.Stages) == 0 && s.Initial != nil {
			t.Fatal("Initial set without any stage")
		}
		for _, p := range s.Errors {
			want := "[" + string(p.Code) + "]"
			if !strings.HasSuffix(p.Error(), want) {
				t.Fatalf("diagnostic %q does not render its code %q", p.Error(), want)
			}
		}
		for _, p := range s.Warnings {
			if p.Error() == "" {
				t.Fatal("warning renders empty")
			}
		}
	})
}
