// Package monshi is the Monshi runtime: it loads definitions, lowers the
// compiled output of the schema package into a private runtime image, and
// interprets that image for case operations.
//
// docs/RUNTIME.md is the specification for everything in this package;
// docs/DESIGN.md remains authoritative for what operations mean, and
// docs/SYNTAX.md for what definitions may say.
//
// The image is not a public API (RUNTIME §9): hosts pass definition source
// to Load and receive an immutable Definition, and case operations arrive
// as methods on the Case handles Create mints from it (RUNTIME §10).
// Tooling that needs definition structure uses the schema package, whose
// model is diagnostic-shaped and human-readable.
package monshi
