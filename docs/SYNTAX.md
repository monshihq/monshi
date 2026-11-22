# Monshi — DSL Syntax Specification

**Status:** Normative reference for the definition language
**Companions:** [DESIGN.md](DESIGN.md) — authoritative for runtime
behavior; [RUNTIME.md](RUNTIME.md) — lowering and interpretation of the
compiled definition
**Scope:** Lexical structure, concrete syntax and static semantics of one
Monshi case definition file

---

## 1. Purpose and conventions

This document is the formal definition of the Monshi DSL described in
DESIGN.md §4. It fixes the lexical grammar, the context-free grammar and
the static (compile-time) semantics that the lexer, recursive-descent
parser and semantic analyzer in `schema/` must implement (DESIGN §4.5).
DESIGN.md remains authoritative for runtime interpretation; this
document is authoritative for what a valid definition file looks like.

Notation and language:

- Grammar rules are written in **EBNF**: terminals in `"double quotes"`,
  `|` alternation, `{ ... }` zero or more repetitions, `[ ... ]`
  optional, `( ... )` grouping. `(* … *)` is a grammar comment.
- **MUST / MUST NOT / SHOULD / MAY** carry RFC 2119 meaning. A rule
  marked `ERROR` or `WARNING` is produced by the compiler, not by the
  runtime.
- Cross-references such as (DESIGN §4.2) point at the design requirement
  a rule implements.

A definition file contains exactly **one** case definition: four
optional-or-mandatory top-level blocks, no outer `case` wrapper
(DESIGN §4). The Host derives the definition ID from the filename stem;
the suggested extension is `.monshi` (see Appendix B). Definitions are
compiled once, immutable afterwards, and never implicitly migrated
(DESIGN §3).

---

## 2. Lexical structure

### 2.1 Source

The source MUST be valid UTF-8. Identifiers and keywords are ASCII-only
and **case-sensitive**; keywords are exactly lowercase. Line endings are
insignificant outside string literals.

Identifiers and casing:

```
IDENT  : letter { letter | digit | "_" } ;
letter : "A"-"Z" | "a"-"z" ;
digit  : "0"-"9" ;
```

Declared names obey a mandatory casing rule (§5.1-N2): **role names and
enum member tags begin with an uppercase letter** (`Applicant`,
`PERSON`); **type, form, stage, group and field names begin with a
lowercase letter** (`legal_form`, `application`, `kind`). This is a
lexical-level contract: the compiler rejects violations, and the DESIGN
§4.1 requirement that role and type names never collide holds
automatically.

### 2.2 Whitespace and comments

Whitespace (space, tab, CR, LF) separates tokens and is otherwise
insignificant. Comments are skipped by the lexer:

```
comment : "//" { any_char }            (* to end of line *)
        | "/*" { any_char } "*/" ;     (* may span lines; not nested *)
```

### 2.3 Tokens

```
INT    : digit { digit } ;
NUMBER : [ "-" ] digit { digit } [ "." digit { digit } ] ;
STRING : '"' { plain_char | escape } '"' ;
escape : '\"' | '\\' | '\n' | '\r' | '\t' ;
```

`plain_char` is any printable character except `"`, `\` and newline:
a string is non-multi-line, and an embedded quote MUST be written `\"`.

- An `IDENT` MUST NOT begin with a digit.
- A `NUMBER` fractional part requires at least one digit after `.`.
  Because a digit must precede the dot, a path segment separator is
  never confused with a decimal point (`application.shareholders` is
  three tokens).
- A leading `.` (relative path marker, §3.6) is punctuation; the lexer
  emits it as a separate token.

Literals and their semantic types:

| Literal | Semantic type |
| --- | --- |
| `"text"` | `string`, `date` or `datetime` |
| `42`, `-7` | `integer` |
| `0.00`, `-12.50` | `decimal` |
| `true`, `false` | `boolean` |
| uppercase-initial `IDENT` in an operand position | enum member tag |

`date`/`datetime` values use ISO 8601 string forms (`"2026-11-22"`,
`"2026-11-22T09:30:00"`), validated at compile time wherever a
`date`/`datetime` literal is accepted. Enum tags are resolved by the
checker (§3.6).

### 2.4 Reserved words

The following tokens are reserved and MUST NOT be used as user-declared
names (type, role, form, stage, group, field or enum tag names). Using
one as a name is an `ERROR`.

- **Block keywords:** `types` `roles` `forms` `stages`
- **Statement keywords:** `allow` `deny` `accept` `reject` `require`
  `all`
- **Permissions:** `view` `edit` `submit` `review` `sign`, plus
  `accept` and `reject` (they are permissions and transition keywords,
  so they appear in both groups)
- **Attributes:** `required` `present` `verifiable` `multiple` `default`
- **Built-in type constructors:** `string` `integer` `decimal` `boolean`
  `date` `datetime` `binary` `enum` `options` `signature`
- **Literals:** `true` `false`

All reserved words are lowercase, so uppercase-initial names (roles,
enum tags) can never collide with them.

### 2.5 Punctuation

`{` `}` `(` `)` `[` `]` `:` `;` `,` `.` `==` `!=` `<` `<=` `>` `>=`
`&&` `||` `!`

Members within a block are juxtaposed without separators: fields end
with `;`; forms, groups, roles, stages and top-level blocks are closed
by `}` and carry **no** trailing `;` and no commas (Appendix B).

---

## 3. Context-free grammar

### 3.1 Definition

```
definition : [ types_block ] [ roles_block ] forms_block stages_block ;
```

- The blocks MUST appear in this order (DESIGN §4 presents this
  canonical order; see Appendix B).
- `types` and `roles` MAY appear at most once and MAY be omitted.
  `forms` and `stages` MUST appear exactly once.
- Duplicate top-level blocks are an `ERROR`.

The grammar itself allows empty blocks; content obligations are static
semantics. A `forms` block declaring no form or a `stages` block
declaring no stage is an `ERROR` (ST1). An empty *form body* is a
different case: legal, with a `WARNING` (§5.3-F1).

### 3.2 Types block

```
types_block : "types" "{" { type_decl } "}" ;
type_decl   : IDENT ":" prim_type ";" ;

prim_type   : "string" | "integer" | "boolean" | "date" | "datetime"
            | "binary"
            | "decimal" "(" INT "," INT ")"
            | "enum" "(" IDENT { "," IDENT } ")" ;
```

A type name is lowercase-initial; an `enum` tag is uppercase-initial
(§5.1-N2). A custom type derives from exactly one primitive built-in
(DESIGN §4.1). `signature` and `options` are NOT legal derivation
bases, and a custom type MUST NOT be used as the base of another custom
type (single-level derivation). See §5.2.

### 3.3 Roles block

```
roles_block   : "roles" "{" { role_decl } "}" ;
role_decl     : IDENT "{" { role_grant } "}" ;

role_grant    : ( "allow" | "deny" ) permission_list grant_targets ";" ;

grant_targets : "all" | grant_path { "," grant_path } ;
grant_path    : IDENT { "." IDENT } ;

permission_list : permission { "," permission } ;
permission      : "view" | "edit" | "submit" | "review" | "sign"
                | "accept" | "reject" ;
```

Role names are uppercase-initial and precede their block with no colon
(DESIGN §4). A `role_grant` MUST carry a target list (`all` or one or
more paths); a role declaration MUST NOT grant `accept` or `reject` —
transition permissions exist only in stage blocks (DESIGN §4.3;
§5.5-P1). Grant paths are **absolute**: the first segment MUST be a
declared form name (§3.6); role and stage blocks have no form anchor,
so a leading `.` in a target cannot be parsed at all. The parser ends
`permission_list` at the first token that is not a permission keyword;
that token begins `grant_targets`.

Conformant excerpt from DESIGN §4.3:

```
roles {
    Applicant {
        allow view, edit application;
        deny edit application.internal;
    }
    Manager {
        allow view all;
    }
}
```

### 3.4 Forms block

```
forms_block  : "forms" "{" { form_decl } "}" ;
form_decl    : IDENT [ "verifiable" ] "{" { member } "}" ;

member       : field_decl | group_decl ;
field_decl   : IDENT ":" value_type { field_attr } ";" ;
group_decl   : IDENT { group_attr } "{" { member } "}" ;

value_type   : prim_type
             | "options"
             | "signature" "(" sig_ref ")"
             | IDENT ;                    (* custom type or role name *)

sig_ref      : operand ;
```

Structural rules:

- **Fields vs. groups:** after the leading `IDENT`, the next token
  decides — `:` means field, an attribute keyword or `{` means group.
  One token of lookahead suffices because attributes are reserved words.
- Groups nest arbitrarily; forms are top level, and only top-level
  forms are independently submitted (DESIGN §4.1).
- Attributes sit between the type and the `;` (fields) or the name and
  the `{` (groups, forms), in any order; see §4 for the validity
  matrix.
- Form, group and field names are lowercase-initial and unique within
  their container (§5.1-N3); the same member name may be reused in
  different forms.
- A lowercase-initial `IDENT` in `value_type` names a custom type; an
  uppercase-initial `IDENT` names a role, making the field
  **role-typed**: the committed value names a user who thereby holds
  that role in this case (DESIGN §4.1). The two namespaces cannot
  collide by casing (§5.1-N2).
- `sig_ref` is the signature authority reference (§5.4-S2).

### 3.5 Stages block

```
stages_block  : "stages" "{" { stage_decl } "}" ;
stage_decl    : IDENT "{" { stage_stmt } "}" ;
stage_stmt    : stage_grant | require_stmt | transition_stmt ;

stage_grant   : ( "allow" | "deny" ) IDENT permission_list
                [ grant_targets ] ";" ;
require_stmt  : "require" IDENT { "," IDENT } ";" ;
transition_stmt : ( "accept" | "reject" ) [ "[" expr "]" ] IDENT ";" ;
```

- Stage names are lowercase-initial; the first declared stage is the
  **initial stage** (DESIGN §4.4). Stage order is therefore semantic.
- `stage_grant` MUST begin with an uppercase-initial declared role
  name — "Stage grants include a role name" (DESIGN §4.3). It MUST
  carry targets unless its permission list consists solely of
  `accept`/`reject` (§5.5-P1).
- `require form [, form …]` names top-level forms and declares the
  stage's completeness invariant: each named form is fully valid and
  submitted, its effective verification units are approved, and its
  required signatures are valid (DESIGN §4.4).
- `transition_stmt` declares an explicit, user-triggered outgoing edge
  to a declared stage. The optional bracketed condition gates the edge.
  Transition conditions are **absolute paths only** (§3.6). `accept`
  checks the destination's `require`; `reject` does not, but still
  requires permission, the declared edge, and its condition
  (DESIGN §4.4).

Conformant excerpt from DESIGN §4.4:

```
stages {
    entry {
        allow Applicant view, edit, submit application;
        allow Manager accept, reject;
        accept[application.kind == PERSON] personal;
        accept[application.kind == COMPANY] corporate;
        reject correction;
    }
    personal {
        require application;
    }
    corporate {
        require application;
    }
    correction {}
}
```

### 3.6 Expressions and path resolution

Expressions occur in `present[...]`, `required[...]`, and transition
conditions. (DESIGN §4.4: "Expressions support Boolean operators,
comparisons, parentheses, paths and literals. No functions are called
inside DSL expressions.")

```
expr       : or_expr ;
or_expr    : and_expr { "||" and_expr } ;
and_expr   : comparison { "&&" comparison } ;
comparison : unary [ comp_op unary ] ;
comp_op    : "==" | "!=" | "<" | "<=" | ">" | ">=" ;
unary      : [ "!" ] primary ;
primary    : "(" expr ")" | operand ;

operand    : STRING | NUMBER | "true" | "false" | path ;
path       : [ "." ] IDENT { "." IDENT } ;
```

The CFG is unambiguous: literals are lexically distinct, and every
name-shaped operand is a `path`. The **checker** (not the parser)
classifies each `path` deterministically from its shape and casing.
This classification governs **condition operands** (inside `present`,
`required` and transition conditions); a `sig_ref` operand inside
`signature(…)` is resolved by §5.4-S2 alone.

- **`. IDENT { "." IDENT }`** (leading dot) — a **relative value
  path**, anchored at the form containing the declaration; the first
  segment MUST be a direct member of that form. Legal in `present` and
  `required` conditions inside `forms` only — anywhere else an `ERROR`
  (no anchor).
- **Single uppercase-initial `IDENT`** — an **enum member tag**; it
  MUST name a declared tag (§5.7-E1). Legal as a comparison operand in
  any expression.
- **`IDENT "." IDENT { "." IDENT }`** — lowercase-initial root, more
  than one segment: an **absolute value path**; the first segment MUST
  be a declared form name. Legal in any expression.
- **Single lowercase-initial `IDENT`** — a bare form reference; forms
  are not values. Always an `ERROR`.

Consequences of the classification:

- `.kind == COMPANY` and `application.kind == COMPANY` are both valid
  inside a form; only the latter is valid in a stage transition.
- Role names are valid operands **nowhere** in expressions (they are
  names of actors, not values); an uppercase-initial operand that does
  not resolve to a declared enum tag is an `ERROR`.
- A path rooted at an uppercase-initial `IDENT` (`Manager.x`) matches
  none of the shapes above and is an `ERROR`.
- Type rules for operands and comparisons are in §5.7.

### 3.7 Paths — DSL vs. data paths

DSL paths (grant targets, expression paths, `sig_ref`) are static
declarations resolved against the definition, in the two forms defined
by §3.6. Runtime data paths additionally address repeated-group
instances by **stable IDs as ordinary segments** — e.g.
`application.shareholders.42`, with no `[id:42]` syntax (DESIGN §5.1).
Data paths belong to the engine API, not to definition files, and are
not part of this grammar.

### 3.8 Attribute syntax summary

```
field_attr  : "required" | "required" "[" expr "]"
            | "present" "[" expr "]"
            | "verifiable"
            | "multiple"
            | "default" operand ;

group_attr  : "required" | "required" "[" expr "]"
            | "present" "[" expr "]"
            | "verifiable"
            | "multiple" ;
```

A `default operand` must be a compile-time constant of the field's type
(§5.3-F5): a literal, or a single uppercase-initial tag of the field's
enum.

```
forms {
    application verifiable {
        kind      : enum(PERSON, COMPANY) required;
        full_name : string required;
        fee       : decimal(14, 2) default 0.00;
        note      : string present[.kind == COMPANY];

        shareholders required[.kind == COMPANY] multiple {
            holder : string required;
            stake  : decimal(14, 2) required;
        }
    }
}
```

---

## 4. Attribute validity matrix

Attributes may appear in any order, and each MUST appear at most once
per declaration; `required` and `required[expr]` are mutually exclusive
variants of the same attribute. An attribute outside this matrix is an
`ERROR` ("invalid attributes", DESIGN §4.5).

| Attribute | field | group | form |
| --- | :-: | :-: | :-: |
| `required` | ✓ | ✓ | ✗ |
| `required[expr]` | ✓ | ✓ | ✗ |
| `present[expr]` | ✓ | ✓ | ✗ |
| `verifiable` | ✓ | ✓ | ✓ |
| `multiple` | ✓ | ✓ | ✗ |
| `default operand` | ✓ ¹ | ✗ | ✗ |

¹ `default` is valid only on fields whose type is scalar and
non-dynamic: `string`, `integer`, `decimal`, `boolean`, `date`,
`datetime`, `enum` (custom or inline). It is invalid on `binary`,
`options`, `signature`, and role-typed fields. An enum default MUST be
a tag of that enum. The operand MUST match the declared type, and
`date`/`datetime` string operands are format-checked at compile time
(§5.3-F5).

Further restrictions:

- On **signature fields** only `required` (unconditional) is valid;
  `present`, `verifiable`, `multiple` and `default` are `ERROR`.
- `required multiple` means *at least one element* (DESIGN §4.1).
  Combined with `required[expr]`, the collection needs one element
  exactly when the condition holds.
- `present[expr]` controls structural presence in the effective state;
  a false condition removes current data at commit without rewriting
  history (DESIGN §4.1).
- Disclosure is governed exclusively by `view` permission — there is no
  visibility attribute, and any name resembling one has no special
  meaning (§2.4).

---

## 5. Static semantics

Every rule below is checked by the semantic analyzer after parsing
(DESIGN §4.5). Violations are `ERROR` unless marked `WARNING`.

### 5.1 Names and namespaces

- **N1** Names MUST NOT be reserved words (§2.4).
- **N2** Casing: role names and enum member tags MUST begin with
  `A–Z`; type, form, stage, group and field names MUST begin with
  `a–z`. Violations are `ERROR`. This enforces the DESIGN §4.1
  role/type collision ban automatically.
- **N3** Namespaces:
  - **Global uppercase namespace** — all role names and all enum tags
    (across every enum) share one namespace and MUST be mutually
    distinct within the definition.
  - **Global root namespace** — form, stage and type names MUST be
    mutually distinct within the definition (they appear as bare
    references in `require`, transition destinations and type
    positions).
  - **Member scope** — field and group names are unique within their
    container (one form body or one group body). The same member name
    MAY be reused under different containers.
- **N4** Resolution: `value_type` lowercase IDENT → custom type;
  uppercase IDENT → role; a name resolving to neither is `ERROR`.
  Path-shaped operands resolve per §3.6. Enum tag lookup happens in
  the global uppercase namespace.

### 5.2 Types

- **T1** A custom type base MUST be a primitive built-in (§3.2);
  `signature`, `options`, custom types and role names MUST NOT appear
  as a base.
- **T2** `decimal(p, s)`: `p` and `s` are non-negative integers,
  `1 ≤ p ≤ 38`, `0 ≤ s ≤ p`.
- **T3** `enum(t1, …, tn)`: `n ≥ 1`; tags are uppercase-initial (N2)
  and globally unique (N3). Tags are stable identifiers of the type;
  renaming a tag changes the data value.
- **T4** A role-typed field's value is a user identity; the field
  grants the named role for the case when committed (DESIGN §4.1).
  Pending working edits never confer the role (§5.5-P6).

### 5.3 Forms, fields, groups

- **F1** An empty form (`{ }`) yields a `WARNING`; a group with no
  members is an `ERROR`.
- **F2** Attributes obey the validity matrix (§4).
- **F3** `verifiable` is not inherited. If a verifiable unit contains a
  descendant that also declares `verifiable`, the parent's verification
  takes precedence; the compiler MUST warn about the redundant
  descendant annotation (DESIGN §4.2).
- **F4** `verifiable multiple` is well-formed: the collection is
  reviewed as a whole, not per member (DESIGN §4.2) — no rule
  violation, recorded for interpretation.
- **F5** A `default` operand MUST be a compile-time constant compatible
  with the field's resolved type: literals per §2.3 (`date`/`datetime`
  string operands MUST parse as ISO 8601), or a single
  uppercase-initial tag belonging to the field's enum. A relative path,
  dotted path or role name in a default is `ERROR`.
- **F6** Conditions in `present`/`required` MUST be boolean expressions
  (§5.7). A condition inside a `multiple` collection applies uniformly
  to the whole collection (instance-relative conditions are deferred,
  Appendix B).
- **F7** Self-reference and cycles: the path set of a condition
  attached to declaration X MUST NOT resolve to X or to any descendant
  of X; the graph over condition-bearing declarations ("X depends on
  Y") MUST be acyclic. Violations are `ERROR`.

### 5.4 Verification targets and signatures

- **S1** Signature fields MUST be declared directly in a form
  (top-level members), not inside groups (DESIGN §4.2).
- **S2** `sig_ref` resolution: an uppercase-initial bare name MUST be a
  declared **role name** (role form); a path — absolute or relative per
  §3.6 — MUST resolve to a **role-typed field of the containing form**
  (assigned form). A single lowercase-initial name is always an `ERROR`
  (write `.reviewer`, not `reviewer`). The reference MUST NOT name the
  signature field itself. Casing (N2/N3) guarantees the two forms can
  never collide. Any unresolvable or mistyped reference is `ERROR`
  ("bad signature references", DESIGN §4.5).
- **S3** A signature field with a role argument permits the first
  eligible holder of that role; with a field argument it resolves the
  authorized user from the committed value of that role-typed field
  (DESIGN §4.2). Neither is checkable beyond resolution at compile
  time.

### 5.5 Roles and permissions

- **P1** Grants obey statement shape (§3.3, §3.5):
  - Role-block grants require targets and MUST NOT mention
    `accept`/`reject`.
  - Stage-block grants require an uppercase-initial declared role name
    first.
  - A statement whose permission list contains `accept` or `reject`
    MUST consist **solely** of them and MUST omit `grant_targets`
    entirely; a statement with targets MUST NOT mention
    `accept`/`reject`. (DESIGN §4.3: target-free and targeted
    permissions cannot be mixed; transitions govern edges, not
    targets.) `ERROR`.
- **P2** Every `grant_path` is absolute (§3.6): first segment =
  declared form name; remaining segments walk declared group/field
  members. `all` denotes the case root. The grammar admits no leading
  `.` in a target (§3.3), so a relative path there is a parse `ERROR`.
- **P3** `submit` targets MUST be top-level forms. `edit` targets MUST
  NOT be signature fields. `review` targets MUST be `verifiable`
  declarations (field, group or form). `sign` targets MUST be
  signature fields. (DESIGN §4.5: "incompatible permissions".)
- **P4** The role named in a `stage_grant` MUST be declared in `roles`.
- **P5** A role that appears in no stage grant, no role-typed field
  and no `signature(Role)` argument is `WARNING` ("unused
  declarations"); its own role-block grants do not count as references.
  A custom type that no field uses is likewise `WARNING`. `allow` and
  `deny` of the same permission on the same target for the same role is
  `WARNING` ("contradictory grants", DESIGN §4.5).
- **P6** Interpretation, for reference (DESIGN §4.3, runtime-checked):
  default-deny; only positive grants grant access; scope inheritance
  Case → Form → Group → Field; explicit deny overrides allow across all
  effective roles and scopes; authority derives solely from committed
  role-typed field values plus the primary role.

### 5.6 Stages and transitions

- **ST1** A definition MUST declare at least one form and at least one
  stage (§3.1); the **first declared stage is the initial stage**
  (DESIGN §4.4).
- **ST2** Transition destinations MUST be declared stage names;
  `require` names MUST be declared top-level form names. Bare
  lowercase references resolve in the global root namespace (N3).
- **ST3** A stage MUST NOT transition to itself, and MUST declare at
  most one edge per destination regardless of kind or condition
  (`ERROR`; the caller selects a destination, so parallel edges to one
  target would be ambiguous).
- **ST4** `require` applies only to top-level forms; requiring another
  stage or a group is `ERROR`.
- **ST5** A stage with no outgoing edges is terminal (legal). A stage
  unreachable from the initial stage through declared edges is
  `WARNING` (DESIGN §4.5).
- **ST6** Transition conditions MUST be boolean expressions (§5.7)
  using absolute paths only (§3.6). A condition that is a compile-time
  constant is `WARNING` ("constant conditions", DESIGN §4.5).

### 5.7 Expressions

- **E1** An uppercase-initial single-segment operand MUST resolve to a
  declared enum tag in the global uppercase namespace (§3.6, N3);
  anything else — role names included — is `ERROR`.
- **E2** Comparison operands MUST be scalar leaf fields: `string`,
  `integer`, `decimal`, `boolean`, `date`, `datetime`, `enum` or custom
  types over these. The following path shapes are `ERROR` as operands:
  `multiple` nodes, paths that traverse a `multiple` ancestor (no
  single value exists per case), groups, forms, `binary`, `signature`
  and role-typed fields, and **`options` fields** — their values come
  from external hooks and are never interpreted by DSL logic (DESIGN
  §4.4: dynamic options are hooks, not language data).
- **E3** Comparison typing: `==`/`!=` require operands of identical
  type, where an enum-typed operand may pair with a tag of **that**
  enum, and two bare tags are legal only if both belong to the **same**
  enum (`PERSON == COMPANY` compiles; tags from different enums are
  `ERROR`; a constant condition over two tags may draw the ST6-class
  warning). `<`, `<=`, `>`, `>=` require `integer`, `decimal`, `date`,
  `datetime` or `string` (lexicographic). Literal pairing: `STRING`
  with `string`, `date`, `datetime` (format-checked per §2.3);
  `NUMBER` with `integer`/`decimal` (respecting precision and scale);
  `true`/`false` with `boolean`; a tag with its `enum`.
- **E4** `&&`, `||`, `!` require boolean operands. A standalone
  `primary` (no `comp_op`) is legal and MUST be boolean: a path to a
  `boolean`-typed field (e.g. `present[.urgent]`); a bare tag or other
  literal standalone is `ERROR`. The whole condition MUST be boolean
  (§5.3-F6, §5.6-ST6).
- **E5** No function calls, index expressions, arithmetic, or field
  access on comparison results exist in the grammar; attempts to write
  them are lexer/parser or checker errors (DESIGN §4.4 — hooks do the
  rest).
- **E6** Absence rule: an operand with no value makes the comparison it
  appears in **unsatisfied** — for `!=` as for `==`. Negation does not
  rescue absence: a negated condition is unsatisfied when any operand it
  reads has no value, so `!` never manufactures structure or a
  requirement out of missing data. Absence is not `Empty`: a defaulted
  field has a value before any condition is evaluated (§4), and an
  emptied field is present with an empty value. Existence operators
  remain undefined (Appendix C).

### 5.8 Compilation output

A definition passing §5 compiles to an immutable in-memory definition;
duplicate definition IDs are rejected at `Load` (DESIGN §3). The
compiler performs no runtime data validation — current case data is
checked by the engine at Update/Submit (DESIGN §4.5).

Resolution is part of the output. Names, paths, types and signature
references are settled, and every `default` and every comparison
literal is a typed constant **normalized to the type of the operand it
pairs with**: an ISO 8601 string compared against `date`/`datetime`
becomes an ordered time value, and an integer literal compared against
`decimal(p, s)` becomes a decimal. Interpretation compares typed values
and never re-parses or re-widens definition text (RUNTIME §3.3).

---

## 6. Parsing notes

The grammar is LL(1) with one token of lookahead and no keyword
collisions:

- Member kind (field vs. group): after the leading `IDENT`, `:` means
  field, an attribute keyword or `{` means group.
- A statement in `stages` is dispatchable on its first token:
  `allow`/`deny` (grant), `require`, `accept`/`reject` (transition).
- `permission_list` ends at the first non-permission token, since all
  permission names are reserved and no path segment or role name can
  be one (§2.4).
- Name-shaped operands all parse as `path`; tags, absolute and relative
  paths are distinguished by the **checker** (§3.6), never by the
  parser.
- `role_grant` vs. `stage_grant` is context-sensitive (block kind), not
  lookahead-dependent.

This matches the planned pipeline: lexer → recursive-descent parser →
AST → semantic analysis → immutable compiled definition (DESIGN §4.5).

---

## 7. Complete example

```
// A registration case. Conforms to docs/SYNTAX.md.

types {
    legal_form : enum(PERSON, COMPANY);
    amount     : decimal(14, 2);
    email      : string;
}

roles {
    Applicant {
        allow view, edit, submit application;
        deny edit application.internal;
    }
    Registrar {
        allow view all;
        allow edit, review application;
    }
    Manager {
        allow view all;
        allow review application;
        allow sign application.approved_by;
        allow edit application.internal;
    }
}

forms {
    application verifiable {
        kind       : legal_form required;
        full_name  : string required;
        contact    : email;
        registered : date default "2026-01-01";
        fee        : amount default 0.00;
        urgent     : boolean default false;
        note       : string;

        reviewer : Manager;                       // role-typed field

        internal present[.kind == COMPANY] {
            risk_score : integer;
        }

        shareholders required[.kind == COMPANY] multiple {
            holder_name : string required;
            shares      : amount required;
        }

        submitted   : signature(Applicant);
        approved_by : signature(.reviewer);
    }

    archive {
        filed_at : datetime;
        note     : string;                     // legal: member names are scoped
    }
}

stages {
    draft {
        allow Applicant view, edit, submit application;
        allow Manager accept, reject;
        accept[application.kind == PERSON] person_review;
        accept[application.kind == COMPANY] company_review;
        reject correction;
    }

    person_review {
        require application;
        allow Manager view all;
        allow Manager accept, reject;
        accept closed;
        reject correction;
    }

    company_review {
        require application;
        allow Registrar view all;
        allow Registrar accept, reject;
        allow Registrar view, edit, submit archive;
        accept closed;
        reject correction;
    }

    correction {
        allow Applicant view, edit, submit application;
        allow Manager accept;
        accept draft;
    }

    closed {}
}
```

Walkthrough of noteworthy shapes: `draft` is the initial stage (first
declared). Conditions inside `forms` use relative paths (`.kind`,
`.reviewer`) while stage transitions use absolute ones
(`application.kind`). `application.note` and `archive.note` coexist
because member names are scoped to their container. `Manager` is
assigned per case by the committed role-typed field
`application.reviewer`; `approved_by` may be signed only by that user,
and `Manager` needs the explicit `sign` grant on that field. The form
is `verifiable` as a whole, so `require application` also demands
Registrar- or Manager-approved review. `shareholders` requires at least
one committed instance exactly when `application.kind == COMPANY`.
`closed` is terminal; every other stage is reachable from `draft`.

---

## Appendix A — Built-in type reference

- **`string`** — UTF-8 scalar text. Default-able.
- **`integer`** — signed 64-bit integer (Appendix B). Default-able.
- **`decimal(p, s)`** — fixed-point decimal with precision `p`, scale
  `s`; `1 ≤ p ≤ 38`, `0 ≤ s ≤ p` (T2). Default-able; the literal MUST
  fit the declared precision and scale.
- **`boolean`** — `true` / `false`. Default-able.
- **`date`** — calendar date (ISO 8601); literal form `"YYYY-MM-DD"`.
  Default-able.
- **`datetime`** — local/UTC timestamp (ISO 8601); literal form
  `"YYYY-MM-DDThh:mm:ss"`. Default-able.
- **`binary`** — opaque `BinaryRef` (DESIGN §5.2); historically
  retained content. Not default-able; not a valid expression operand
  (§5.7-E2).
- **`enum(t1, …, tn)`** — closed choice set; the value is one tag.
  Default-able with a tag of that enum. Tags are uppercase-initial and
  globally unique (N2/N3).
- **`options`** — dynamic choice set; choices always come from the
  `Options` hook at the field path (DESIGN §7). Not default-able;
  **never valid in DSL expressions** — external values are not
  interpreted by the language (§5.7-E2). A missing hook is a runtime
  error, not silent success.
- **`signature(sig_ref)`** — an effective signature per content
  version; at most one effective signature per content version. Not
  default-able; see §5.4 for the reference rules.
- **`RoleName`** — role assignment; the value is a committed user
  identity, and the holder gains the role for this case (DESIGN §4.1).
  Uppercase-initial type position naming a declared role (§5.1-N4). Not
  default-able; not a valid expression operand (§5.7-E2).

## Appendix B — Choices not fixed by DESIGN.md

DESIGN.md specifies the language's shapes but not every concrete token.
The following are **normative decisions made by this document**, chosen
to stay faithful to the design examples (which already use uppercase
`Applicant`/`PERSON` and lowercase `application`/`entry`); revisit them
before the compiler milestones freeze:

1. **Group syntax**: `name [attrs] { … }` — colonless like
   forms/roles/stages; no `group` keyword; no trailing `;` after any
   block.
2. **Block order/obligation**: canonical order `types` → `roles` →
   `forms` → `stages`; `forms` and `stages` mandatory, each non-empty.
3. **Enum member syntax**: inline parameter list `enum(A, B)`; a named
   alias is declared in `types`.
4. **`options` takes no parameters**; its choices are always
   hook-provided by field path, and `options` values never appear in
   expressions (§5.7-E2).
5. **`default <operand>`** placement (after type), literal forms,
   quoted ISO 8601 for `date`/`datetime`, and compile-time format
   checking.
6. **Operator spellings**: `&&` `||` `!` and the six comparison
   operators; precedence per §3.6.
7. **Comments**: `//` and `/* … */`.
8. **Casing constraints**: role names and enum tags uppercase-initial;
   all other names lowercase-initial (N2). This replaces DESIGN §4.1's
   collision ban with a lexical guarantee.
9. **Dot-relative paths**: `.name…` anchored at the containing form,
   legal only inside `forms` (conditions, `sig_ref`); everything
   outside `forms` — grant targets, transition conditions — is
   absolute-only (§3.6).
10. **Namespace partition**: global uppercase namespace (roles ∪ enum
    tags), global root namespace (forms ∪ stages ∪ types), member names
    scoped to their container (N3) — enabling name reuse across
    containers.
11. **Uniform conditions inside `multiple` collections**; per-instance
    (relative-path) conditions are deferred.
12. **Statement placement**: `accept`/`reject` grants only in stage
    blocks, always sole and target-free (P1); role grants always
    targeted.
13. **One edge per destination per stage** (ST3).
14. **`.monshi` file extension** as the host-side convention
    (definition ID derives from the stem, DESIGN §3).
15. **Attribute placement**: attributes sit after the type (fields) or
    after the name (groups, forms), in free order; the §4 validity
    matrix governs per-node validity (DESIGN lists attributes but not
    their validity per declaration beyond signature fields).
16. Value ranges for `integer` (signed 64-bit) and the `decimal`
    precision ceiling of 38 (T2).

## Appendix C — Open points (intentionally undefined)

- Instance-relative condition paths inside `multiple` collections (e.g.
  per-element `present`). Note: dot-relative paths (§3.6) are anchored
  at the **form**, not at the nearest group or repeated element.
- Existence operators for optional nodes in expressions (currently
  only value comparisons; `present`/`required` cover structural need).
- Namespacing or documentation attributes (labels, placeholders, UI
  metadata). UI text is a host concern; the engine supplies the
  structural view (DESIGN §6.1).
- Any extension mechanism for type constraints (lengths, regexes,
  ranges) — the DSL stays free of them; enforce through `Validate`
  hooks.
