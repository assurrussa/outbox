package querybuilder

import (
	sq "github.com/Masterminds/squirrel"
)

// Eq is syntactic sugar for use with Where/Having/Set methods.
type Eq sq.Eq

// BuilderDollar default statement for pgsql sq.Dollar format.
func BuilderDollar() sq.StatementBuilderType {
	return sq.StatementBuilder.PlaceholderFormat(sq.Dollar)
}
