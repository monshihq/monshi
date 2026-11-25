package program

import "strconv"

// dec and quote render the two things a refusal names: a revision and a
// host-supplied identifier. Quoting matters more than it looks — an empty
// definition ID is a real mistake a host makes, and "definition """" is
// legible where "definition " is not.
func dec(v uint64) string { return strconv.FormatUint(v, 10) }

func quote(s string) string { return strconv.Quote(s) }
