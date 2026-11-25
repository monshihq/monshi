// Package monshi compiles workflow definitions and runs cases against them.
// A Runtime registers definitions, opens actor-specific Case handles, and
// connects case operations to storage and host hooks. Handles do not cache
// mutable case state.
//
// Hosts use Load or Runtime.Define to compile source. Tools that need to
// inspect the checked language model can use the schema package directly.
package monshi
