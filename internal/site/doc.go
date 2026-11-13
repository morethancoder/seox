// Package site is seox's area for crawler, audit, page and keywords.
//
// It owns the ops: audit, page, keywords. Attach each handler with
// ops.Handle("<op name>", fn) in this package's init(); until then the op
// answers not_implemented. Do not edit another area's package.
package site

func init() {
	// Handlers are registered here by the area builder.
}
