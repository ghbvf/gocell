package scanner

import "go/ast"

// ReceiverTypeName extracts the base type name from a receiver type expression.
// It handles the following forms:
//
//   - *T      (*ast.StarExpr wrapping *ast.Ident)
//   - T       (*ast.Ident)
//   - T[P]    (*ast.IndexExpr — single type parameter)
//   - T[P,Q]  (*ast.IndexListExpr — multiple type parameters)
//
// Any other form (e.g. anonymous struct receivers) returns the empty string.
func ReceiverTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return ReceiverTypeName(e.X)
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		// Single type parameter: T[P]
		if id, ok := e.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.IndexListExpr:
		// Multiple type parameters: T[P, Q]
		if id, ok := e.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}
