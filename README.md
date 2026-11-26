# Monshi

Monshi is an embeddable Go engine for cases such as registrations, claims, and
applications. BPMN models the flow of activities; CMMN models case work that
evolves as information arrives. Monshi is useful when you're building a Go
application and the questions you need to answer are about the case itself:
**What can this person edit? What's missing? Can the case move forward?**
You declare fields, permissions, and stages together, and the engine checks
those rules when your application reads or changes a case.

## Quick start: file a registration

An applicant can edit and submit a registration, but can only move it from
`draft` to `filed` once the required name has been submitted:

```text
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
```

Run the [Go example](examples/registration/main.go) from the repository root.
It uses the in-memory runtime; no database or service is needed.

```sh
go run ./examples/registration
```

```text
Cannot file: missing registration.name
Submitted registration at revision 1
Stage: filed (revision 2)
```

The first `Transition` returns the missing field's path. `Update` fills it in
the applicant's working copy; `Submit` publishes the form. Retrying the same
transition at the submitted revision succeeds because the **committed** case
now meets the destination's requirement.

## Learn more

- [Language reference](docs/SYNTAX.md): fields, roles, conditions, and stages.
- [Case operations](case.go) and [design](docs/DESIGN.md): drafts, review, signatures, and history.
- [Storage contract](storage.go) and [compatibility notes](docs/STORAGE.md): persist cases in your own backend.
- [Runtime internals](docs/RUNTIME.md): execution and cost model.

## License

[MIT License](LICENSE) · Copyright © 2026 AslRousta <me@alirus.ir>
