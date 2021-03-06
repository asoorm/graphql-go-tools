//go:generate stringer -type=Keyword
package keyword

type Keyword int

const (
	UNDEFINED Keyword = iota
	IDENT
	EXTEND
	COMMENT
	EOF

	COLON
	BANG
	LINETERMINATOR
	TAB
	SPACE
	COMMA
	AT
	DOT
	SPREAD
	PIPE
	SLASH
	EQUALS
	NEGATIVESIGN
	AND
	ON
	QUOTE

	IMPLEMENTS
	SCHEMA
	SCALAR
	TYPE
	INTERFACE
	UNION
	ENUM
	INPUT
	DIRECTIVE

	VARIABLE
	STRING
	INTEGER
	FLOAT
	TRUE
	FALSE
	NULL
	QUERY
	MUTATION
	SUBSCRIPTION
	FRAGMENT

	BRACKETOPEN
	BRACKETCLOSE
	SQUAREBRACKETOPEN
	SQUAREBRACKETCLOSE
	CURLYBRACKETOPEN
	CURLYBRACKETCLOSE
)
