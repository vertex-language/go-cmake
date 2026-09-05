# CMake Language Grammar

The CMake language is essentially an AST of command invocations, each with a list of arguments.
Unlike Make, which is line-based, CMake is structured strictly by parentheses, similar to Lisp but without nesting of commands.

## Tokens

- `Identifier`: `[A-Za-z_][A-Za-z0-9_]*`
- `Unquoted argument`: any text not containing whitespace, `(`, `)`, `#`, `"`, or `\` (unless escaped)
- `Quoted argument`: `"..."`
- `Bracket argument`: `[[...]]`, `[=[...]=]`, `[==[...]==]`, etc.
- `Line comment`: `#` followed by text until newline
- `Bracket comment`: `#[=[...]=]`

## Syntax

```ebnf
file         ::= file_element*
file_element ::= command_invocation line_ending |
                 (bracket_comment|space)* line_ending

line_ending  ::= line_comment? newline

space        ::= <match '[ \t]+'>

newline      ::= <match '\n'>

command_invocation  ::= space* identifier space* '(' arguments ')'

arguments    ::= argument? separated_arguments*
separated_arguments ::= separation+ argument? |
                        separation* '(' arguments ')'

separation   ::= space | line_ending

argument     ::= bracket_argument | quoted_argument | unquoted_argument
```

## Macro and Variable Expansion

Variables and macros are expanded before the command is evaluated. Expansion takes the form of:
- `${VAR}` — standard variable reference
- `$ENV{VAR}` — environment variable reference
- `$CACHE{VAR}` — cache variable reference
- `$<GENEX>` — generator expression (expanded at generate/build time, not configure time)

Argument lists can be joined with `;` in unquoted arguments to form lists.
