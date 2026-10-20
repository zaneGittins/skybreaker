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
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
)

// Request is the operation a request runs.
type Request struct {
	Op     string   // "query", "mutation" or "subscription"
	Name   string   // the operation's name, if any
	Fields []string // top-level field names (not aliases), in order
	// Indirect is set when the operation's top level spreads a fragment or
	// has an inline fragment, so Fields doesn't list everything it calls.
	Indirect bool
	// Calls are the top-level fields with their arguments, in order.
	Calls []Call

	variables map[string]any // the body's "variables", decoded strictly
}

// Call is one top-level field and its arguments.
type Call struct {
	Field string
	Args  map[string]Value
}

// Value is an argument value as written in the document.
type Value struct {
	Kind   ValueKind
	Str    string // KindString: the decoded string; KindVar: the variable's name
	Fields map[string]Value
	List   []Value
}

type ValueKind int

const (
	KindOther  ValueKind = iota // a number, boolean or enum
	KindString                  // a string or block string, decoded
	KindVar                     // $name
	KindObject
	KindList
	KindNull
)

// maxDepth bounds nesting of selection sets, arguments and values.
const maxDepth = 100

// Parse reads a JSON GraphQL request body: {"query", "operationName",
// "variables"}.
func Parse(body []byte) (Request, error) {
	doc, opName, vars, err := envelope(body)
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
		picked[0].variables = vars
		return picked[0], nil
	case opName == "":
		return Request{}, fmt.Errorf("graphql: %d operations and no operationName", len(ops))
	case len(picked) == 0:
		return Request{}, fmt.Errorf("graphql: no operation named %q", opName)
	}
	return Request{}, fmt.Errorf("graphql: %d operations named %q", len(picked), opName)
}

// envelope decodes the JSON body, refusing duplicate keys: two parsers
// could each pick a different "query", or a different title in the
// variables.
func envelope(body []byte) (doc, opName string, vars map[string]any, err error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil {
		return "", "", nil, fmt.Errorf("graphql: body: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return "", "", nil, errors.New("graphql: body must be one JSON object")
	}
	seen := map[string]bool{}
	hasQuery := false
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return "", "", nil, fmt.Errorf("graphql: body: %w", err)
		}
		key, _ := tok.(string)
		if seen[key] {
			return "", "", nil, fmt.Errorf("graphql: duplicate %q", key)
		}
		seen[key] = true
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return "", "", nil, fmt.Errorf("graphql: body: %w", err)
		}
		switch key {
		case "query":
			if err := json.Unmarshal(raw, &doc); err != nil {
				return "", "", nil, errors.New("graphql: query must be a string")
			}
			hasQuery = true
		case "operationName":
			if string(raw) != "null" {
				if err := json.Unmarshal(raw, &opName); err != nil {
					return "", "", nil, errors.New("graphql: operationName must be a string")
				}
			}
		case "variables":
			v, err := strictJSON(raw)
			if err != nil {
				return "", "", nil, fmt.Errorf("graphql: variables: %w", err)
			}
			switch v := v.(type) {
			case nil:
			case map[string]any:
				vars = v
			default:
				return "", "", nil, errors.New("graphql: variables must be an object")
			}
		}
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return "", "", nil, fmt.Errorf("graphql: body: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", "", nil, errors.New("graphql: text after the JSON object")
	}
	if !hasQuery {
		return "", "", nil, errors.New("graphql: no query")
	}
	return doc, opName, vars, nil
}

// strictJSON decodes one JSON value, refusing duplicate object keys at any
// depth. Numbers stay json.Number.
func strictJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := strictValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("text after the value")
	}
	return v, nil
}

func strictValue(dec *json.Decoder, depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("nested too deeply")
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch d {
	case '{':
		m := map[string]any{}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, _ := k.(string)
			if _, dup := m[key]; dup {
				return nil, fmt.Errorf("duplicate key %q", key)
			}
			if m[key], err = strictValue(dec, depth+1); err != nil {
				return nil, err
			}
		}
		_, err := dec.Token()
		return m, err
	case '[':
		var l []any
		for dec.More() {
			v, err := strictValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			l = append(l, v)
		}
		_, err := dec.Token()
		return l, err
	}
	return nil, fmt.Errorf("unexpected %v", d)
}

// Strings returns the string fields keys of a call's object argument arg,
// with variables resolved: nil for a key that is absent or null. It fails
// when it can't tell: arg missing or not an object, a key that isn't a
// string, or a variable the body doesn't give a value for (a default in
// the document would apply, which isn't read here).
func (r Request) Strings(c Call, arg string, keys ...string) (map[string]*string, error) {
	v, ok := c.Args[arg]
	if !ok {
		return nil, fmt.Errorf("graphql: %s has no %s argument", c.Field, arg)
	}
	obj, err := r.resolve(v)
	if err != nil {
		return nil, err
	}
	m, ok := obj.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("graphql: %s's %s is not an object", c.Field, arg)
	}
	out := map[string]*string{}
	for _, k := range keys {
		switch s := m[k].(type) {
		case nil:
			out[k] = nil
		case string:
			out[k] = &s
		default:
			return nil, fmt.Errorf("graphql: %s's %s.%s is not a string", c.Field, arg, k)
		}
	}
	return out, nil
}

// resolve turns a Value into plain data: string, map[string]any, []any, nil,
// or Value{Kind: KindOther} for anything else.
func (r Request) resolve(v Value) (any, error) {
	switch v.Kind {
	case KindString:
		return v.Str, nil
	case KindNull:
		return nil, nil
	case KindVar:
		x, ok := r.variables[v.Str]
		if !ok {
			return nil, fmt.Errorf("graphql: variable $%s has no value in the body", v.Str)
		}
		return x, nil
	case KindObject:
		m := map[string]any{}
		for k, f := range v.Fields {
			x, err := r.resolve(f)
			if err != nil {
				return nil, err
			}
			m[k] = x
		}
		return m, nil
	case KindList:
		var l []any
		for _, e := range v.List {
			x, err := r.resolve(e)
			if err != nil {
				return nil, err
			}
			l = append(l, x)
		}
		return l, nil
	}
	return v, nil
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
		call := Call{Field: field, Args: map[string]Value{}}
		if p.is(punct, "(") {
			if call.Args, err = p.arguments(depth); err != nil {
				return err
			}
		}
		op.Calls = append(op.Calls, call)
	} else if p.is(punct, "(") {
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

// arguments reads ( name: value ... ), refusing an argument named twice.
func (p *parser) arguments(depth int) (map[string]Value, error) {
	if err := p.expect("("); err != nil {
		return nil, err
	}
	args := map[string]Value{}
	for !p.is(punct, ")") {
		if p.tok.kind == eof {
			return nil, errors.New("graphql: unclosed arguments")
		}
		n, err := p.name()
		if err != nil {
			return nil, err
		}
		if _, dup := args[n]; dup {
			return nil, fmt.Errorf("graphql: argument %q given twice", n)
		}
		if err := p.expect(":"); err != nil {
			return nil, err
		}
		if args[n], err = p.value(depth + 1); err != nil {
			return nil, err
		}
	}
	if len(args) == 0 {
		return nil, errors.New("graphql: empty arguments")
	}
	return args, p.advance()
}

// value reads one argument value.
func (p *parser) value(depth int) (Value, error) {
	if depth > maxDepth {
		return Value{}, errors.New("graphql: nested too deeply")
	}
	switch {
	case p.is(punct, "$"):
		if err := p.advance(); err != nil {
			return Value{}, err
		}
		n, err := p.name()
		return Value{Kind: KindVar, Str: n}, err
	case p.is(punct, "["):
		if err := p.advance(); err != nil {
			return Value{}, err
		}
		v := Value{Kind: KindList}
		for !p.is(punct, "]") {
			if p.tok.kind == eof {
				return Value{}, errors.New("graphql: unclosed list")
			}
			e, err := p.value(depth + 1)
			if err != nil {
				return Value{}, err
			}
			v.List = append(v.List, e)
		}
		return v, p.advance()
	case p.is(punct, "{"):
		if err := p.advance(); err != nil {
			return Value{}, err
		}
		v := Value{Kind: KindObject, Fields: map[string]Value{}}
		for !p.is(punct, "}") {
			if p.tok.kind == eof {
				return Value{}, errors.New("graphql: unclosed object")
			}
			n, err := p.name()
			if err != nil {
				return Value{}, err
			}
			if _, dup := v.Fields[n]; dup {
				return Value{}, fmt.Errorf("graphql: field %q given twice", n)
			}
			if err := p.expect(":"); err != nil {
				return Value{}, err
			}
			if v.Fields[n], err = p.value(depth + 1); err != nil {
				return Value{}, err
			}
		}
		return v, p.advance()
	case p.tok.kind == value && strings.HasPrefix(p.tok.text, `"`):
		s, err := decodeString(p.tok.text)
		if err != nil {
			return Value{}, err
		}
		return Value{Kind: KindString, Str: s}, p.advance()
	case p.tok.kind == value:
		return Value{Kind: KindOther}, p.advance()
	case p.is(name, "null"):
		return Value{Kind: KindNull}, p.advance()
	case p.tok.kind == name:
		return Value{Kind: KindOther}, p.advance()
	}
	return Value{}, fmt.Errorf("graphql: unexpected %q in a value", p.tok.text)
}

// decodeString turns a string token, quotes included, into its value.
func decodeString(tok string) (string, error) {
	if strings.HasPrefix(tok, `"""`) {
		return blockString(strings.ReplaceAll(tok[3:len(tok)-3], `\"""`, `"""`)), nil
	}
	var b strings.Builder
	s := tok[1 : len(tok)-1]
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(s) {
			return "", errors.New("graphql: bad escape")
		}
		switch s[i] {
		case '"', '\\', '/':
			b.WriteByte(s[i])
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'u':
			r, n, err := unicodeEscape(s[i+1:])
			if err != nil {
				return "", err
			}
			b.WriteRune(r)
			i += n
		default:
			return "", fmt.Errorf("graphql: bad escape \\%c", s[i])
		}
	}
	return b.String(), nil
}

// unicodeEscape reads what follows \u: XXXX, a surrogate pair of them, or
// {X...}. It returns the rune and how many bytes it used.
func unicodeEscape(s string) (rune, int, error) {
	bad := errors.New("graphql: bad unicode escape")
	if strings.HasPrefix(s, "{") {
		end := strings.IndexByte(s, '}')
		if end < 2 || end > 9 {
			return 0, 0, bad
		}
		v, err := strconv.ParseUint(s[1:end], 16, 32)
		if err != nil || v > unicode.MaxRune || v >= 0xd800 && v <= 0xdfff {
			return 0, 0, bad
		}
		return rune(v), end + 1, nil
	}
	hex4 := func(s string) (rune, bool) {
		if len(s) < 4 {
			return 0, false
		}
		v, err := strconv.ParseUint(s[:4], 16, 16)
		return rune(v), err == nil
	}
	r, ok := hex4(s)
	if !ok {
		return 0, 0, bad
	}
	if utf16.IsSurrogate(r) {
		if r >= 0xdc00 || !strings.HasPrefix(s[4:], `\u`) {
			return 0, 0, bad
		}
		lo, ok := hex4(s[6:])
		if !ok || lo < 0xdc00 || lo > 0xdfff {
			return 0, 0, bad
		}
		return utf16.DecodeRune(r, lo), 10, nil
	}
	return r, 4, nil
}

// blockString applies GraphQL's BlockStringValue: common indentation and
// leading and trailing blank lines go.
func blockString(raw string) string {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n"), "\n")
	common := -1
	for i, l := range lines {
		if i == 0 {
			continue
		}
		indent := len(l) - len(strings.TrimLeft(l, " \t"))
		if indent < len(l) && (common < 0 || indent < common) {
			common = indent
		}
	}
	if common > 0 {
		for i := 1; i < len(lines); i++ {
			if len(lines[i]) >= common {
				lines[i] = lines[i][common:]
			} else {
				lines[i] = ""
			}
		}
	}
	blank := func(l string) bool { return strings.TrimLeft(l, " \t") == "" }
	for len(lines) > 0 && blank(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
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
