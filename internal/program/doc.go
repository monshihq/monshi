// Package program compiles a checked schema into an immutable execution
// image. It owns slot numbering, type and permission tables, compiled
// conditions, and the indexes used to query them. Returned slices are
// borrowed, read-only views. A Program contains no mutable case state.
package program
