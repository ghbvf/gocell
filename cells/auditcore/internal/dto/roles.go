// Package dto contains auditcore-local typed views and shared constants.
//
// Per cell-patterns.md "DTO 作用域三档", auditcore's dto package holds Cell-B
// scope items: shared across multiple slices of auditcore but NOT cross-cell
// wire types. Auditcore is a pure subscriber — cross-cell event payload
// decoding lives inside slice packages (e.g. auditappend), and auditcore
// emits no events of its own that other cells consume. Role names are
// runtime auth values, not contract wire data, so they belong here rather
// than in a cross-cell shared package.
package dto
