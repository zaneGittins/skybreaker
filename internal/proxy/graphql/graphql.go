// Package graphql reads just enough of a GraphQL request for the broker to
// decide on it: which operation runs, whether it's a query or a mutation,
// and which top-level fields it calls. A mutation's top-level fields are
// what it does (mergePullRequest, deleteRef); everything below them is data.
//
// It fails closed: a body it can't read unambiguously (duplicate JSON keys,
// several operations and no operationName, unbalanced or unterminated
// tokens, deep nesting) is an error, never a guess.
package graphql

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Request is the operation a request runs.
type Request struct {
	Op     string   // "query", "mutation" or "subscription"
	Name   string   // the operation's name, if any
	Fields []string // top-level field names (not aliases), in order
	// Indirect is set when the operation's top level spreads a fragment or
	// has an inline fragment, so Fields doesn't list everything it calls.
	Indirect bool
}

// maxDepth bounds nesting of selection sets, arguments and values.
const maxDepth = 100

// Parse reads a JSON GraphQL request body: {"query", "operationName",
// "variables"}.
func Parse(body []byte) (Request, error) {
	doc, opName, err := envelope(body)
	if err != nil {
		return Request{}, err
	}
	ops, err := parseDocument(doc)
	if err != nil {
		return Request{}, err
	}
	var picked []Request
	for _, op := range ops {
		if opName == "" || op.Name == opName {
			picked = append(picked, op)
		}
	}
	switch {
	case len(picked) == 1:
		return picked[0], nil
	case opName == "":
		return Request{}, fmt.Errorf("graphql: %d operations and no operationName", len(ops))
	case len(picked) == 0:
		return Request{}, fmt.Errorf("graphql: no operation named %q", opName)
	}
	return Request{}, fmt.Errorf("graphql: %d operations named %q", len(picked), opName)
}

// envelope decodes the JSON body, refusing duplicate keys: two parsers
// could each pick a different "query".
func envelope(body []byte) (doc, opName string, err error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil {
		return "", "", fmt.Errorf("graphql: body: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return "", "", errors.New("graphql: body must be one JSON object")
	}
	seen := map[string]bool{}
	hasQuery := false
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return "", "", fmt.Errorf("graphql: body: %w", err)
		}
		key, _ := tok.(string)
		if seen[key] {
			return "", "", fmt.Errorf("graphql: duplicate %q", key)
		}
		seen[key] = true
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return "", "", fmt.Errorf("graphql: body: %w", err)
		}
		switch key {
		case "query":
			if err := json.Unmarshal(raw, &doc); err != nil {
				return "", "", errors.New("graphql: query must be a string")
			}
			hasQuery = true
		case "operationName":
			if string(raw) != "null" {
				if err := json.Unmarshal(raw, &opName); err != nil {
					return "", "", errors.New("graphql: operationName must be a string")
				}
			}
		}
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return "", "", fmt.Errorf("graphql: body: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", "", errors.New("graphql: text after the JSON object")
	}
	if !hasQuery {
		return "", "", errors.New("graphql: no query")
	}
	return doc, opName, nil
}

// --- lexer -----------------------------------------------------------------

type kind int

const (
	eof kind = iota
	punct
	name
	value // a number or string, skipped as a unit
)

type token struct {
	kind kind
	text string
}

type lexer struct {
	s   string
	pos int
}

func (l *lexer) next() (token, error) {
	for l.pos < len(l.s) {
		c := l.s[l.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',':
			l.pos++
		case strings.HasPrefix(l.s[l.pos:], "\ufeff"):
			l.pos += len("\ufeff")
		case c == '#':
			for l.pos < len(l.s) && l.s[l.pos] != '\n' && l.s[l.pos] != '\r' {
				l.pos++
			}
		default:
			return l.token()
		}
	}
	return token{kind: eof}, nil
}

func isNameStart(c byte) bool { return c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }
func isNameChar(c byte) bool  { return isNameStart(c) || c >= '0' && c <= '9' }

func (l *lexer) token() (token, error) {
	start, c := l.pos, l.s[l.pos]
	switch {
	case strings.ContainsRune("!$&()=:@[]{}|", rune(c)):
		l.pos++
		return token{punct, string(c)}, nil
	case strings.HasPrefix(l.s[l.pos:], "..."):
		l.pos += 3
		return token{punct, "..."}, nil
	case isNameStart(c):
		for l.pos < len(l.s) && isNameChar(l.s[l.pos]) {
			l.pos++
		}
		return token{name, l.s[start:l.pos]}, nil
	case c == '-' || c >= '0' && c <= '9':
		l.pos++
		for l.pos < len(l.s) && (isNameChar(l.s[l.pos]) || strings.ContainsRune(".+-", rune(l.s[l.pos]))) {
			l.pos++
		}
		return token{value, l.s[start:l.pos]}, nil
	case strings.HasPrefix(l.s[l.pos:], `"""`):
		l.pos += 3
		for {
			switch {
			case l.pos >= len(l.s):
				return token{}, errors.New("graphql: unterminated block string")
			case strings.HasPrefix(l.s[l.pos:], `\"""`):
				l.pos += 4
			case strings.HasPrefix(l.s[l.pos:], `"""`):
				l.pos += 3
				return token{value, l.s[start:l.pos]}, nil
			default:
				l.pos++
			}
		}
	case c == '"':
		l.pos++
		for {
			if l.pos >= len(l.s) || l.s[l.pos] == '\n' || l.s[l.pos] == '\r' {
				return token{}, errors.New("graphql: unterminated string")
			}
			switch l.s[l.pos] {
			case '\\':
				l.pos += 2
			case '"':
				l.pos++
				return token{value, l.s[start:l.pos]}, nil
			default:
				l.pos++
			}
		}
	}
	return token{}, fmt.Errorf("graphql: unexpected character %q at %d", c, start)
}

// --- parser ----------------------------------------------------------------

type parser struct {
	lx  lexer
	tok token
}

func (p *parser) advance() error {
	t, err := p.lx.next()
	p.tok = t
	return err
}

func (p *parser) is(k kind, text string) bool { return p.tok.kind == k && p.tok.text == text }

func (p *parser) expect(text string) error {
	if !p.is(punct, text) {
		return fmt.Errorf("graphql: expected %q, found %q", text, p.tok.text)
	}
	return p.advance()
}

func (p *parser) name() (string, error) {
	if p.tok.kind != name {
		return "", fmt.Errorf("graphql: expected a name, found %q", p.tok.text)
	}
	n := p.tok.text
	return n, p.advance()
}

func parseDocument(doc string) ([]Request, error) {
	p := &parser{lx: lexer{s: doc}}
	for i := 0; i < len(doc); i++ {
		// SourceCharacter: tab, line breaks and nothing else below space.
		if c := doc[i]; c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			return nil, fmt.Errorf("graphql: control character at %d", i)
		}
	}
	if err := p.advance(); err != nil {
		return nil, err
	}
	var ops []Request
	defs := 0
	for p.tok.kind != eof {
		defs++
		switch {
		case p.is(punct, "{"):
			op := Request{Op: "query"}
			if err := p.selectionSet(&op, 1); err != nil {
				return nil, err
			}
			ops = append(ops, op)
		case p.is(name, "query"), p.is(name, "mutation"), p.is(name, "subscription"):
			op := Request{Op: p.tok.text}
			if err := p.advance(); err != nil {
				return nil, err
			}
			if p.tok.kind == name {
				op.Name = p.tok.text
				if err := p.advance(); err != nil {
					return nil, err
				}
			}
			if p.is(punct, "(") {
				if err := p.skipGroup(1); err != nil {
					return nil, err
				}
			}
			if err := p.directives(1); err != nil {
				return nil, err
			}
			if err := p.selectionSet(&op, 1); err != nil {
				return nil, err
			}
			ops = append(ops, op)
		case p.is(name, "fragment"):
			if err := p.advance(); err != nil {
				return nil, err
			}
			if n, err := p.name(); err != nil || n == "on" {
				return nil, errors.New("graphql: bad fragment name")
			}
			if !p.is(name, "on") {
				return nil, errors.New("graphql: fragment without a type condition")
			}
			if err := p.advance(); err != nil {
				return nil, err
			}
			if _, err := p.name(); err != nil {
				return nil, err
			}
			if err := p.directives(1); err != nil {
				return nil, err
			}
			if err := p.selectionSet(nil, 1); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("graphql: unexpected %q", p.tok.text)
		}
	}
	if len(ops) == 0 {
		return nil, errors.New("graphql: no operation")
	}
	return ops, nil
}

// selectionSet reads { selections }. op, when set, is the operation whose
// top level this is, and collects its field names.
func (p *parser) selectionSet(op *Request, depth int) error {
	if depth > maxDepth {
		return errors.New("graphql: nested too deeply")
	}
	if err := p.expect("{"); err != nil {
		return err
	}
	n := 0
	for !p.is(punct, "}") {
		if p.tok.kind == eof {
			return errors.New("graphql: unclosed selection set")
		}
		if err := p.selection(op, depth); err != nil {
			return err
		}
		n++
	}
	if n == 0 {
		return errors.New("graphql: empty selection set")
	}
	return p.advance()
}

func (p *parser) selection(op *Request, depth int) error {
	if p.is(punct, "...") {
		if op != nil {
			op.Indirect = true
		}
		if err := p.advance(); err != nil {
			return err
		}
		if p.tok.kind == name && p.tok.text != "on" {
			if err := p.advance(); err != nil { // fragment spread
				return err
			}
			return p.directives(depth)
		}
		if p.is(name, "on") { // inline fragment's type condition
			if err := p.advance(); err != nil {
				return err
			}
			if _, err := p.name(); err != nil {
				return err
			}
		}
		if err := p.directives(depth); err != nil {
			return err
		}
		return p.selectionSet(nil, depth+1)
	}
	field, err := p.name()
	if err != nil {
		return err
	}
	if p.is(punct, ":") { // alias: the field is the second name
		if err := p.advance(); err != nil {
			return err
		}
		if field, err = p.name(); err != nil {
			return err
		}
	}
	if op != nil {
		op.Fields = append(op.Fields, field)
	}
	if p.is(punct, "(") {
		if err := p.skipGroup(depth); err != nil {
			return err
		}
	}
	if err := p.directives(depth); err != nil {
		return err
	}
	if p.is(punct, "{") {
		return p.selectionSet(nil, depth+1)
	}
	return nil
}

func (p *parser) directives(depth int) error {
	for p.is(punct, "@") {
		if err := p.advance(); err != nil {
			return err
		}
		if _, err := p.name(); err != nil {
			return err
		}
		if p.is(punct, "(") {
			if err := p.skipGroup(depth); err != nil {
				return err
			}
		}
	}
	return nil
}

// skipGroup skips a balanced ( ... ): arguments or variable definitions,
// with any lists and objects inside them.
func (p *parser) skipGroup(depth int) error {
	closers := map[string]string{"(": ")", "[": "]", "{": "}"}
	var stack []string
	for {
		if p.tok.kind == eof {
			return errors.New("graphql: unclosed group")
		}
		if p.tok.kind == punct {
			if c, ok := closers[p.tok.text]; ok {
				stack = append(stack, c)
				if depth+len(stack) > maxDepth {
					return errors.New("graphql: nested too deeply")
				}
			} else if strings.Contains(")]}", p.tok.text) {
				if len(stack) == 0 || stack[len(stack)-1] != p.tok.text {
					return fmt.Errorf("graphql: unbalanced %q", p.tok.text)
				}
				stack = stack[:len(stack)-1]
			}
		}
		if err := p.advance(); err != nil {
			return err
		}
		if len(stack) == 0 {
			return nil
		}
	}
}
