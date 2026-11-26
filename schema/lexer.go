package schema

import "unicode/utf8"

// lex tokenizes one definition source. It recovers from invalid
// characters and bad escapes, and stops only at an unterminated string or
// block comment, which leave the remainder uninterpretable.
func lex(src string) ([]token, []Problem) {
	l := &lexer{rs: []rune(src), line: 1, col: 1}
	l.run()
	// A fatal lexical error (unterminated string or block comment) stops
	// run() early; the parser still requires a final tkEOF sentinel.
	if n := len(l.toks); n == 0 || l.toks[n-1].kind != tkEOF {
		l.toks = append(l.toks, token{kind: tkEOF, line: l.line, col: l.col})
	}
	return l.toks, l.problems
}

type lexer struct {
	rs        []rune
	j         int
	line, col int
	toks      []token
	problems  []Problem
}

func (l *lexer) done() bool { return l.j >= len(l.rs) }
func (l *lexer) cur() rune  { return l.rs[l.j] }
func (l *lexer) next() rune {
	r := l.rs[l.j]
	l.j++
	if r == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return r
}

func (l *lexer) problem(code Code, ctx string) {
	l.problems = append(l.problems, Problem{Line: l.line, Col: l.col, Code: code, Context: ctx})
}

func (l *lexer) emit(k kind, text string, line, col int) {
	l.toks = append(l.toks, token{kind: k, text: text, line: line, col: col})
}

func isASCIILetter(r rune) bool {
	return ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z')
}

func isDigit(r rune) bool { return '0' <= r && r <= '9' }

func isIdentChar(r rune) bool { return isASCIILetter(r) || isDigit(r) || r == '_' }

func (l *lexer) run() {
	for !l.done() {
		r := l.cur()
		switch {
		case r == '\n' || r == ' ' || r == '\t' || r == '\r':
			l.next()
		case r == '/' && l.peekAt(1) == '/':
			for !l.done() && l.cur() != '\n' {
				l.next()
			}
		case r == '/' && l.peekAt(1) == '*':
			if !l.skipBlockComment() {
				return
			}
		case isASCIILetter(r):
			l.scanIdent()
		case isDigit(r) || (r == '-' && isDigit(l.peekAt(1))):
			l.scanNumber()
		case r == '"':
			if !l.scanString() {
				return
			}
		default:
			if !l.scanPunct() {
				l.problem(CodeInvalidCharacter, string(r))
				l.next()
			}
		}
	}
	l.toks = append(l.toks, token{kind: tkEOF, line: l.line, col: l.col})
}

func (l *lexer) peekAt(k int) rune {
	if l.j+k < len(l.rs) {
		return l.rs[l.j+k]
	}
	return 0
}

func (l *lexer) skipBlockComment() bool {
	line, col := l.line, l.col
	l.next() // '/'
	l.next() // '*'
	for {
		if l.done() {
			l.problems = append(l.problems, Problem{Line: line, Col: col, Code: CodeUnterminatedComment})
			return false
		}
		if l.cur() == '*' && l.peekAt(1) == '/' {
			l.next()
			l.next()
			return true
		}
		l.next()
	}
}

func (l *lexer) scanIdent() {
	line, col := l.line, l.col
	start := l.j
	for !l.done() && isIdentChar(l.cur()) {
		l.next()
	}
	l.emit(tkIdent, string(l.rs[start:l.j]), line, col)
}

func (l *lexer) scanNumber() {
	line, col := l.line, l.col
	start := l.j
	if l.cur() == '-' {
		l.next()
	}
	for !l.done() && isDigit(l.cur()) {
		l.next()
	}
	if !l.done() && l.cur() == '.' && isDigit(l.peekAt(1)) {
		l.next()
		for !l.done() && isDigit(l.cur()) {
			l.next()
		}
	}
	l.emit(tkNumber, string(l.rs[start:l.j]), line, col)
}

// scanString unescapes into token text; returns false when the string is
// unterminated (a fatal, unrecoverable lexical error).
func (l *lexer) scanString() bool {
	line, col := l.line, l.col
	l.next() // opening quote
	var text []rune
	for {
		if l.done() || l.cur() == '\n' {
			l.problems = append(l.problems, Problem{Line: line, Col: col, Code: CodeUnterminatedString})
			return false
		}
		r := l.next()
		if r == '"' {
			l.emit(tkString, string(text), line, col)
			return true
		}
		if r != '\\' {
			text = append(text, r)
			continue
		}
		if l.done() || l.cur() == '\n' {
			l.problems = append(l.problems, Problem{Line: line, Col: col, Code: CodeUnterminatedString})
			return false
		}
		e := l.next()
		switch e {
		case '"', '\\':
			text = append(text, e)
		case 'n':
			text = append(text, '\n')
		case 'r':
			text = append(text, '\r')
		case 't':
			text = append(text, '\t')
		default:
			l.problems = append(l.problems, Problem{Line: l.line, Col: l.col - 1, Code: CodeInvalidEscape, Context: `\` + string(e)})
			text = append(text, e)
		}
	}
}

func (l *lexer) scanPunct() bool {
	for _, two := range []string{"==", "!=", "<=", ">=", "&&", "||"} {
		if l.peekAt(0) == rune(two[0]) && l.peekAt(1) == rune(two[1]) {
			line, col := l.line, l.col
			l.next()
			l.next()
			l.emit(tkPunct, two, line, col)
			return true
		}
	}
	r := l.cur()
	if r < utf8.RuneSelf && isSinglePunct(r) {
		l.emit(tkPunct, string(r), l.line, l.col)
		l.next()
		return true
	}
	return false
}

func isSinglePunct(r rune) bool {
	switch r {
	case '{', '}', '(', ')', '[', ']', ':', ';', ',', '.', '<', '>', '!':
		return true
	}
	return false
}
