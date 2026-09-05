package eval

import "context"

// cmdNoOp accepts and ignores a command whose effect is outside this
// implementation's model. It is what a command is registered as when CMake
// does something this package deliberately does not, and accepting it silently
// is better than failing a configure over a directive that changes nothing
// here.
func cmdNoOp(context.Context, *evaluator, []Arg) error { return nil }
