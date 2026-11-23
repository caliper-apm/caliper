package symbolicator

import (
	"fmt"
	"strconv"
	"strings"
)

// demangleSwift decodes Swift 5+ (and 4.2/4.0) mangled linkage names ($s..., _$s..., $S..., _$S..., _T0...)
// into human-readable Swift signatures using a zero-dependency Clean-Room stack AST parser.
// Returns ("", false) when the input is not a recognized Swift symbol or cannot be cleanly decoded,
// allowing DemangleNativeSymbol to fall back honestly without fabricating frames.
func demangleSwift(raw string) (string, bool) {
	body := raw
	switch {
	case strings.HasPrefix(body, "_$s"):
		body = body[3:]
	case strings.HasPrefix(body, "$s"):
		body = body[2:]
	case strings.HasPrefix(body, "_$S"):
		body = body[3:]
	case strings.HasPrefix(body, "$S"):
		body = body[2:]
	case strings.HasPrefix(body, "_T0"):
		body = body[3:]
	default:
		return "", false
	}
	if body == "" {
		return "", false
	}

	p := &swiftDemangler{src: body}
	if !p.parse() {
		return "", false
	}
	out := p.renderTop()
	if out == "" {
		return "", false
	}
	return out, true
}

type swiftNodeKind int

const (
	snIdent swiftNodeKind = iota
	snType
	snNominal
	snEntity
	snTuple
	snFuncType
	snFirstElemMarker
	snEmptyList
	snPrivateDiscrim
	snGenericConstraint
	snConformance
	snSpecMarker
)

type swiftTupleElem struct {
	Label string
	Type  string
}

type swiftNode struct {
	kind       swiftNodeKind
	text       string
	nominalTag byte // 'C', 'V', 'O', 'P', 'a'
	tupleElems []swiftTupleElem
	funcParams []swiftTupleElem
	funcRet    string
	isAsync    bool
	isThrows   bool
	isEscaping bool
}

type swiftDemangler struct {
	src   string
	pos   int
	stack []swiftNode
	subs  []swiftNode
	words []string
}

func (p *swiftDemangler) push(n swiftNode) {
	p.stack = append(p.stack, n)
}

func (p *swiftDemangler) pop() (swiftNode, bool) {
	if len(p.stack) == 0 {
		return swiftNode{}, false
	}
	n := p.stack[len(p.stack)-1]
	p.stack = p.stack[:len(p.stack)-1]
	return n, true
}

func (p *swiftDemangler) addSub(n swiftNode) {
	p.subs = append(p.subs, n)
}

func (p *swiftDemangler) peek() byte {
	if p.pos >= len(p.src) {
		return 0
	}
	return p.src[p.pos]
}

func (p *swiftDemangler) peekAt(offset int) byte {
	if p.pos+offset >= len(p.src) {
		return 0
	}
	return p.src[p.pos+offset]
}

func (p *swiftDemangler) parse() bool {
	for p.pos < len(p.src) {
		before := p.pos
		ch := p.src[p.pos]

		switch {
		case ch >= '0' && ch <= '9':
			ident, ok := p.parseIdentifier()
			if !ok {
				return false
			}
			// Check for private file discriminator suffix `LL`.
			if strings.HasPrefix(p.src[p.pos:], "LL") {
				p.pos += 2
				p.push(swiftNode{kind: snPrivateDiscrim, text: ident})
			} else {
				node := swiftNode{kind: snIdent, text: ident}
				p.push(node)
				p.addSub(node)
			}

		case ch == '_':
			p.pos++
			p.push(swiftNode{kind: snFirstElemMarker, text: "_"})

		case ch == 'y':
			// Special `yp` = Any, `yXl` = Swift.AnyObject, otherwise empty list / ().
			if p.peekAt(1) == 'p' {
				p.pos += 2
				p.push(swiftNode{kind: snType, text: "Any"})
			} else if strings.HasPrefix(p.src[p.pos:], "yXl") {
				p.pos += 3
				p.push(swiftNode{kind: snType, text: "Swift.AnyObject"})
			} else {
				p.pos++
				p.push(swiftNode{kind: snEmptyList, text: "()"})
			}

		case ch == 's':
			p.pos++
			p.push(swiftNode{kind: snIdent, text: "Swift"})

		case ch == 'S':
			if !p.parseStdSubstitution() {
				return false
			}

		case ch == 'A':
			if !p.parseBackReference() {
				return false
			}

		case ch == 'C' || ch == 'V' || ch == 'O' || ch == 'P' || ch == 'a':
			p.pos++
			if !p.applyNominal(ch) {
				return false
			}

		case ch == 'E':
			p.pos++
			if !p.applyExtension() {
				return false
			}

		case ch == 'G':
			p.pos++
			if !p.applyBoundGeneric() {
				return false
			}

		case ch == 't':
			p.pos++
			if !p.applyTuple() {
				return false
			}

		case ch == 'c' || ch == 'K':
			p.pos++
			if !p.applyFunctionType(false, ch == 'K', false) {
				return false
			}

		case ch == 'Y':
			if !p.parseAsyncOrConcurrency() {
				return false
			}

		case ch == 'X':
			if !p.parseFunctionAttribute() {
				return false
			}

		case ch == 'F':
			if p.peekAt(1) == 'Z' {
				p.pos += 2
				if !p.applyFunctionDecl(true) {
					return false
				}
			} else {
				p.pos++
				if !p.applyFunctionDecl(false) {
					return false
				}
			}

		case ch == 'f':
			if !p.parseFunctionOrAccessor() {
				return false
			}

		case ch == 'v':
			if !p.parseVariableDecl() {
				return false
			}

		case ch == 'i':
			if !p.parseSubscriptDecl() {
				return false
			}

		case ch == 'z':
			p.pos++
			if top, ok := p.pop(); ok {
				p.push(swiftNode{kind: snType, text: "inout " + renderNodeType(top)})
			} else {
				return false
			}

		case ch == 'd':
			p.pos++
			if top, ok := p.pop(); ok {
				p.push(swiftNode{kind: snType, text: renderNodeType(top) + "..."})
			} else {
				return false
			}

		case ch == 'p':
			// Protocol existential type (`<proto>_p` or `<proto>p`)
			p.pos++
			if !p.applyExistential() {
				return false
			}

		case ch == 'x':
			p.pos++
			p.push(swiftNode{kind: snType, text: "A"})

		case ch == 'q':
			if !p.parseGenericParam() {
				return false
			}

		case ch == 'R':
			if !p.parseGenericRequirement() {
				return false
			}

		case ch == 'l':
			p.pos++
			p.applyGenericSignature()

		case ch == 'T':
			if !p.parseThunkOrSpecialization() {
				return false
			}

		case ch == 'M':
			if !p.parseMetadata() {
				return false
			}

		case ch == 'N':
			p.pos++
			if top, ok := p.pop(); ok {
				p.push(swiftNode{kind: snEntity, text: "type metadata for " + top.text})
			}

		case ch == 'm':
			// Metatype `m`: `T.Type`
			p.pos++
			if top, ok := p.pop(); ok {
				p.push(swiftNode{kind: snType, text: renderNodeType(top) + ".Type"})
			}

		case ch == 'o':
			// Operator identifier: skip 2-char operator tag like oi/op/oP when attached to ident
			if p.pos+2 <= len(p.src) {
				p.pos += 2
			} else {
				return false
			}

		case ch == 'L':
			// Local decl index `L<n>_` or `L_`
			p.pos++
			p.skipIndex()

		case ch == 'u':
			// Generic signature prefix marker on dependent generic type `u...r`
			p.pos++

		case ch == 'r':
			// Dependent generic type terminator
			p.pos++

		default:
			return false
		}

		if p.pos == before {
			return false
		}
	}
	return len(p.stack) > 0
}

func (p *swiftDemangler) parseIdentifier() (string, bool) {
	if p.pos >= len(p.src) {
		return "", false
	}
	// Word-substituted compressed identifier starts with '0'.
	if p.src[p.pos] == '0' {
		p.pos++
		var sb strings.Builder
		for p.pos < len(p.src) {
			ch := p.src[p.pos]
			if ch == '0' {
				p.pos++
				break
			}
			if ch >= '1' && ch <= '9' {
				lit, ok := p.readLengthPrefixed()
				if !ok {
					return "", false
				}
				sb.WriteString(lit)
				p.addWords(lit)
				continue
			}
			if ch >= 'a' && ch <= 'z' {
				idx := int(ch - 'a')
				if idx >= len(p.words) {
					return "", false
				}
				p.pos++
				sb.WriteString(p.words[idx])
				continue
			}
			if ch >= 'A' && ch <= 'Z' {
				idx := int(ch - 'A')
				if idx >= len(p.words) {
					return "", false
				}
				p.pos++
				sb.WriteString(p.words[idx])
				// Uppercase marks the LAST word-substitution; followed by '0' or one trailing literal.
				if p.pos < len(p.src) {
					next := p.src[p.pos]
					if next == '0' {
						p.pos++
					} else if next >= '1' && next <= '9' {
						lit, ok := p.readLengthPrefixed()
						if !ok {
							return "", false
						}
						sb.WriteString(lit)
						p.addWords(lit)
					}
				}
				break
			}
			return "", false
		}
		if sb.Len() == 0 {
			return "", false
		}
		return sb.String(), true
	}

	lit, ok := p.readLengthPrefixed()
	if !ok {
		return "", false
	}
	p.addWords(lit)
	return lit, true
}

func (p *swiftDemangler) readLengthPrefixed() (string, bool) {
	start := p.pos
	for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
		p.pos++
	}
	if p.pos == start {
		return "", false
	}
	n, err := strconv.Atoi(p.src[start:p.pos])
	if err != nil || n <= 0 || p.pos+n > len(p.src) {
		return "", false
	}
	// Swift escapes identifiers starting with digits or '_' using an extra '_' if needed,
	// but length-prefixed slice already contains the exact n bytes.
	ident := p.src[p.pos : p.pos+n]
	p.pos += n
	return ident, true
}

// addWords extracts camelCase / underscore-delimited words (>= 2 chars) into Swift's
// 26-slot word substitution table per Swift ABI mangling rules.
func (p *swiftDemangler) addWords(ident string) {
	if len(p.words) >= 26 || len(ident) < 2 {
		return
	}
	i := 0
	n := len(ident)
	for i < n && len(p.words) < 26 {
		for i < n && !isWordChar(ident[i]) {
			i++
		}
		if i >= n {
			break
		}
		start := i
		if isUpper(ident[i]) {
			i++
			// Check if acronym (multiple consecutive uppercase/digits) or TitleCase.
			if i < n && (isUpper(ident[i]) || isDigit(ident[i])) {
				for i < n && (isUpper(ident[i]) || isDigit(ident[i])) {
					// Acronym ends before the last uppercase letter if followed by a lowercase letter (e.g. URLSession -> URL + Session).
					if isUpper(ident[i]) && i+1 < n && isLower(ident[i+1]) {
						break
					}
					i++
				}
			} else {
				for i < n && (isLower(ident[i]) || isDigit(ident[i])) {
					i++
				}
			}
		} else {
			for i < n && (isLower(ident[i]) || isDigit(ident[i])) {
				i++
			}
		}
		if i-start >= 2 {
			p.words = append(p.words, ident[start:i])
		}
	}
}

func isWordChar(c byte) bool { return isUpper(c) || isLower(c) || isDigit(c) }
func isUpper(c byte) bool    { return c >= 'A' && c <= 'Z' }
func isLower(c byte) bool    { return c >= 'a' && c <= 'z' }
func isDigit(c byte) bool    { return c >= '0' && c <= '9' }

func (p *swiftDemangler) parseStdSubstitution() bool {
	p.pos++ // skip 'S'
	if p.pos >= len(p.src) {
		return false
	}
	// Repeat count syntax, e.g., `S2b` => two `Sb` (Swift.Bool), `S2i` => two `Si`.
	repeat := 1
	if p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
		start := p.pos
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			p.pos++
		}
		n, err := strconv.Atoi(p.src[start:p.pos])
		if err != nil || n < 1 || n > 32 || p.pos >= len(p.src) {
			return false
		}
		repeat = n
	}

	ch := p.src[p.pos]
	p.pos++

	// Optional suffix `Sg`: wraps top type on stack as `T?`.
	if ch == 'g' {
		for r := 0; r < repeat; r++ {
			top, ok := p.pop()
			if !ok {
				return false
			}
			t := renderNodeType(top)
			if strings.Contains(t, " -> ") {
				t = "(" + t + ")"
			}
			opt := swiftNode{kind: snType, text: t + "?"}
			p.push(opt)
		}
		return true
	}

	// Objective-C / C imported module `So` or `SC`
	if ch == 'o' {
		for r := 0; r < repeat; r++ {
			p.push(swiftNode{kind: snIdent, text: "__C"})
		}
		return true
	}
	if ch == 'C' {
		for r := 0; r < repeat; r++ {
			p.push(swiftNode{kind: snIdent, text: "__C_Synthesized"})
		}
		return true
	}

	stdName, ok := swiftStdTypes[ch]
	if !ok {
		return false
	}
	for r := 0; r < repeat; r++ {
		p.push(swiftNode{kind: snType, text: stdName})
	}
	return true
}

var swiftStdTypes = map[byte]string{
	'a': "Swift.Array",
	'b': "Swift.Bool",
	'c': "Swift.Unicode.Scalar",
	'd': "Swift.Double",
	'D': "Swift.Dictionary",
	'f': "Swift.Float",
	'h': "Swift.Set",
	'I': "Swift.DefaultIndices",
	'i': "Swift.Int",
	'J': "Swift.Character",
	'N': "Swift.ClosedRange",
	'n': "Swift.Range",
	'O': "Swift.ObjectIdentifier",
	'P': "Swift.UnsafePointer",
	'p': "Swift.UnsafeMutablePointer",
	'q': "Swift.Optional",
	'Q': "Swift.OpaquePointer",
	'R': "Swift.UnsafeBufferPointer",
	'r': "Swift.UnsafeMutableBufferPointer",
	'S': "Swift.String",
	's': "Swift.Substring",
	'u': "Swift.UInt",
	'V': "Swift.UnsafeRawPointer",
	'v': "Swift.UnsafeMutableRawPointer",
	'W': "Swift.UnsafeRawBufferPointer",
	'w': "Swift.UnsafeMutableRawBufferPointer",
}

func (p *swiftDemangler) parseBackReference() bool {
	p.pos++ // skip 'A'
	if p.pos >= len(p.src) {
		return false
	}
	// Numeric substitution `A<n>_`
	if p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
		idx, ok := p.readNaturalIndex()
		if !ok || idx >= len(p.subs) {
			return false
		}
		p.push(p.subs[idx])
		return true
	}
	// Multi-substitution run: lowercase letters continue, uppercase terminates.
	for p.pos < len(p.src) {
		ch := p.src[p.pos]
		if ch >= 'a' && ch <= 'z' {
			idx := int(ch - 'a')
			if idx >= len(p.subs) {
				return false
			}
			p.pos++
			p.push(p.subs[idx])
			continue
		}
		if ch >= 'A' && ch <= 'Z' {
			idx := int(ch - 'A')
			if idx >= len(p.subs) {
				return false
			}
			p.pos++
			p.push(p.subs[idx])
			return true
		}
		return false
	}
	return false
}

func (p *swiftDemangler) applyNominal(tag byte) bool {
	if len(p.stack) < 2 {
		return false
	}
	// Optional private discriminator sitting on top of name.
	var discrim string
	if p.stack[len(p.stack)-1].kind == snPrivateDiscrim {
		d, _ := p.pop()
		discrim = d.text
	}
	name, ok1 := p.pop()
	ctx, ok2 := p.pop()
	if !ok1 || !ok2 {
		return false
	}
	memberName := name.text
	if discrim != "" {
		memberName = fmt.Sprintf("(%s in %s)", memberName, discrim)
	}
	qualified := ctx.text + "." + memberName
	node := swiftNode{kind: snNominal, text: qualified, nominalTag: tag}
	p.push(node)
	p.addSub(node)
	return true
}

func (p *swiftDemangler) applyExtension() bool {
	if len(p.stack) < 2 {
		return false
	}
	var genericWhere string
	if p.stack[len(p.stack)-1].kind == snGenericConstraint {
		gc, _ := p.pop()
		genericWhere = gc.text
	}
	if len(p.stack) < 2 {
		return false
	}
	mod, _ := p.pop()
	target, _ := p.pop()
	text := fmt.Sprintf("(extension in %s):%s", mod.text, target.text)
	if genericWhere != "" {
		text += "<" + genericWhere + ">"
	}
	node := swiftNode{kind: snNominal, text: text}
	p.push(node)
	return true
}

func (p *swiftDemangler) applyBoundGeneric() bool {
	// Generic args sit after an `snEmptyList` (`y`) marker, with the base type directly below `y`.
	markIdx := -1
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].kind == snEmptyList {
			markIdx = i
			break
		}
	}
	if markIdx < 1 {
		return false
	}
	argNodes := p.stack[markIdx+1:]
	base := p.stack[markIdx-1]
	p.stack = p.stack[:markIdx-1]

	args := make([]string, 0, len(argNodes))
	for _, a := range argNodes {
		args = append(args, renderNodeType(a))
	}

	var rendered string
	switch {
	case base.text == "Swift.Array" && len(args) == 1:
		rendered = "[" + args[0] + "]"
	case base.text == "Swift.Dictionary" && len(args) == 2:
		rendered = "[" + args[0] + " : " + args[1] + "]"
	case base.text == "Swift.Optional" && len(args) == 1:
		rendered = args[0] + "?"
	default:
		rendered = base.text + "<" + strings.Join(args, ", ") + ">"
	}
	node := swiftNode{kind: snType, text: rendered}
	p.push(node)
	return true
}

func (p *swiftDemangler) applyTuple() bool {
	// In Swift 5 mangling, `_` (snFirstElemMarker) sits right AFTER the first tuple element:
	// [elem0, _, elem1, elem2] + 't' -> (elem0, elem1, elem2).
	// For 1-element tuple: [elem0, _] + 't' -> (elem0).
	markIdx := -1
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].kind == snFirstElemMarker {
			markIdx = i
			break
		}
	}
	if markIdx < 1 {
		return false
	}
	first := p.stack[markIdx-1]
	rest := p.stack[markIdx+1:]
	p.stack = p.stack[:markIdx-1]

	elems := make([]swiftTupleElem, 0, 1+len(rest))
	elems = append(elems, swiftTupleElem{Type: renderNodeType(first)})
	for _, r := range rest {
		elems = append(elems, swiftTupleElem{Type: renderNodeType(r)})
	}
	p.push(swiftNode{
		kind:       snTuple,
		text:       formatTupleElems(elems),
		tupleElems: elems,
	})
	return true
}

func (p *swiftDemangler) applyFunctionType(isAsync, isThrows, isEscaping bool) bool {
	if len(p.stack) < 2 {
		return false
	}
	paramNode, _ := p.pop()
	retNode, _ := p.pop()
	params := extractParams(paramNode)
	ret := renderNodeType(retNode)
	ft := swiftNode{
		kind:       snFuncType,
		funcParams: params,
		funcRet:    ret,
		isAsync:    isAsync,
		isThrows:   isThrows,
		isEscaping: isEscaping,
	}
	ft.text = formatFuncSig(params, ret, isAsync, isThrows)
	p.push(ft)
	return true
}

func (p *swiftDemangler) parseAsyncOrConcurrency() bool {
	if p.pos+2 > len(p.src) {
		return false
	}
	switch p.src[p.pos+1] {
	case 'a':
		p.pos += 2
		isThrows := false
		if p.peek() == 'K' {
			isThrows = true
			p.pos++
		}
		if p.peek() == 'c' {
			p.pos++
			return p.applyFunctionType(true, isThrows, false)
		}
		return p.applyFunctionType(true, isThrows, false)
	case 'b':
		// `Yb` = @Sendable function attribute; ignore cleanly
		p.pos += 2
		return true
	default:
		return false
	}
}

func (p *swiftDemangler) parseFunctionAttribute() bool {
	if p.pos+2 > len(p.src) {
		return false
	}
	code := p.src[p.pos+1]
	p.pos += 2
	switch code {
	case 'E', 'B', 'C', 'b', 'c', 'f', 'e', 'g', 'h', 'i', 'l', 'm', 'o', 'w', 'u':
		// Function conventions / ownership qualifiers (escaping, block, C, unowned, weak).
		// When `XE` follows a function type, no change to core signature needed; if `Xc` / `XB`
		// acts as a function type constructor, synthesize it if top isn't already snFuncType.
		if (code == 'B' || code == 'C') && (len(p.stack) == 0 || p.stack[len(p.stack)-1].kind != snFuncType) {
			return p.applyFunctionType(false, false, false)
		}
		return true
	default:
		return true
	}
}

func (p *swiftDemangler) applyFunctionDecl(isStatic bool) bool {
	// Pop function type (or synthesize from [ret, params] when F directly follows params/ret).
	if len(p.stack) < 2 {
		return false
	}
	var ft swiftNode
	if p.stack[len(p.stack)-1].kind == snFuncType {
		ft, _ = p.pop()
	} else {
		if len(p.stack) < 3 {
			return false
		}
		paramNode, _ := p.pop()
		retNode, _ := p.pop()
		ft = swiftNode{
			kind:       snFuncType,
			funcParams: extractParams(paramNode),
			funcRet:    renderNodeType(retNode),
		}
	}

	// Pop optional parameter labels matching len(ft.funcParams).
	labels := p.popParamLabels(len(ft.funcParams))

	// Pop function name (plus optional private discriminator) and enclosing context.
	var discrim string
	if len(p.stack) > 0 && p.stack[len(p.stack)-1].kind == snPrivateDiscrim {
		d, _ := p.pop()
		discrim = d.text
	}
	nameNode, ok1 := p.pop()
	ctxNode, ok2 := p.pop()
	if !ok1 || !ok2 {
		return false
	}
	fnName := nameNode.text
	if discrim != "" {
		fnName = fmt.Sprintf("(%s in %s)", fnName, discrim)
	}

	labeledParams := applyLabels(ft.funcParams, labels)
	prefix := ""
	if isStatic {
		prefix = "static "
	}
	sig := formatFuncSig(labeledParams, ft.funcRet, ft.isAsync, ft.isThrows)
	full := fmt.Sprintf("%s%s.%s%s", prefix, ctxNode.text, fnName, sig)
	p.push(swiftNode{kind: snEntity, text: full})
	return true
}

func (p *swiftDemangler) parseFunctionOrAccessor() bool {
	if p.pos+2 > len(p.src) {
		return false
	}
	sub := p.src[p.pos+1]
	p.pos += 2
	switch sub {
	case 'C', 'c':
		// Initializer: fC = __allocating_init (class) or init (struct/enum), fc = init
		if len(p.stack) < 2 {
			return false
		}
		var ft swiftNode
		if p.stack[len(p.stack)-1].kind == snFuncType {
			ft, _ = p.pop()
		} else {
			paramNode, _ := p.pop()
			retNode, _ := p.pop()
			ft = swiftNode{
				kind:       snFuncType,
				funcParams: extractParams(paramNode),
				funcRet:    renderNodeType(retNode),
			}
		}
		labels := p.popParamLabels(len(ft.funcParams))
		ctxNode, ok := p.pop()
		if !ok {
			return false
		}
		initName := "init"
		if sub == 'C' && ctxNode.nominalTag == 'C' {
			initName = "__allocating_init"
		}
		labeledParams := applyLabels(ft.funcParams, labels)
		sig := formatFuncSig(labeledParams, ft.funcRet, ft.isAsync, ft.isThrows)
		p.push(swiftNode{kind: snEntity, text: fmt.Sprintf("%s.%s%s", ctxNode.text, initName, sig)})
		return true

	case 'D', 'd':
		ctxNode, ok := p.pop()
		if !ok {
			return false
		}
		name := "deinit"
		if sub == 'D' {
			name = "__deallocating_deinit"
		}
		p.push(swiftNode{kind: snEntity, text: ctxNode.text + "." + name})
		return true

	case 'U':
		// Closure: `fU_` (closure #1) or `fU<n>_` (closure #<n+2>)
		idx := p.readClosureIndex()
		ft, ok1 := p.pop()
		parent, ok2 := p.pop()
		if !ok1 || !ok2 {
			return false
		}
		sig := ft.text
		if ft.kind == snFuncType {
			sig = formatFuncSig(ft.funcParams, ft.funcRet, ft.isAsync, ft.isThrows)
		}
		p.push(swiftNode{
			kind: snEntity,
			text: fmt.Sprintf("closure #%d %s in %s", idx, sig, parent.text),
		})
		return true

	case 'g', 's', 'm', 'M', 'r', 'w', 'W':
		return p.applyAccessor(sub, false)

	default:
		return false
	}
}

func (p *swiftDemangler) parseVariableDecl() bool {
	if p.pos+2 > len(p.src) {
		return false
	}
	p.pos++ // skip 'v'
	isStatic := false
	if p.peek() == 'Z' {
		isStatic = true
		p.pos++
	}
	if p.pos >= len(p.src) {
		return false
	}
	sub := p.src[p.pos]
	p.pos++
	switch sub {
	case 'g', 's', 'M', 'r', 'w', 'W', 'm':
		if p.peek() == 'Z' {
			isStatic = true
			p.pos++
		}
		return p.applyAccessor(sub, isStatic)
	case 'p':
		// Property metadata/init (`vpfi`, `vpMV`, `vpWvd`, `vpZ`, `vp`)
		var tag string
		if strings.HasPrefix(p.src[p.pos:], "fi") {
			p.pos += 2
			tag = "variable initialization expression of "
		} else if strings.HasPrefix(p.src[p.pos:], "MV") {
			p.pos += 2
			tag = "property descriptor for "
		} else if strings.HasPrefix(p.src[p.pos:], "Wvd") {
			p.pos += 3
			tag = "direct field offset for "
		} else if p.peek() == 'Z' {
			p.pos++
			isStatic = true
		}
		return p.applyVariableEntity(tag, isStatic)
	default:
		return p.applyVariableEntity("", isStatic)
	}
}

func (p *swiftDemangler) applyAccessor(sub byte, isStatic bool) bool {
	if len(p.stack) < 3 {
		return false
	}
	typNode, _ := p.pop()
	var discrim string
	if len(p.stack) > 0 && p.stack[len(p.stack)-1].kind == snPrivateDiscrim {
		d, _ := p.pop()
		discrim = d.text
	}
	nameNode, ok1 := p.pop()
	ctxNode, ok2 := p.pop()
	if !ok1 || !ok2 {
		return false
	}
	accName := map[byte]string{
		'g': "getter",
		's': "setter",
		'M': "modify",
		'm': "materializeForSet",
		'r': "read",
		'w': "willSet",
		'W': "didSet",
	}[sub]
	prop := nameNode.text
	if discrim != "" {
		prop = fmt.Sprintf("(%s in %s)", prop, discrim)
	}
	prefix := ""
	if isStatic {
		prefix = "static "
	}
	p.push(swiftNode{
		kind: snEntity,
		text: fmt.Sprintf("%s%s.%s.%s : %s", prefix, ctxNode.text, prop, accName, renderNodeType(typNode)),
	})
	return true
}

func (p *swiftDemangler) applyVariableEntity(prefixTag string, isStatic bool) bool {
	if len(p.stack) < 3 {
		return false
	}
	typNode, _ := p.pop()
	var discrim string
	if len(p.stack) > 0 && p.stack[len(p.stack)-1].kind == snPrivateDiscrim {
		d, _ := p.pop()
		discrim = d.text
	}
	nameNode, ok1 := p.pop()
	ctxNode, ok2 := p.pop()
	if !ok1 || !ok2 {
		return false
	}
	prop := nameNode.text
	if discrim != "" {
		prop = fmt.Sprintf("(%s in %s)", prop, discrim)
	}
	staticPrefix := ""
	if isStatic {
		staticPrefix = "static "
	}
	p.push(swiftNode{
		kind: snEntity,
		text: fmt.Sprintf("%s%s%s.%s : %s", prefixTag, staticPrefix, ctxNode.text, prop, renderNodeType(typNode)),
	})
	return true
}

func (p *swiftDemangler) parseSubscriptDecl() bool {
	if p.pos+2 > len(p.src) {
		return false
	}
	p.pos++ // skip 'i'
	sub := p.src[p.pos]
	p.pos++
	var acc string
	var prefix string
	switch sub {
	case 'g':
		acc = ".getter"
	case 's':
		acc = ".setter"
	case 'M':
		acc = ".modify"
	case 'r':
		acc = ".read"
	case 'p':
		if strings.HasPrefix(p.src[p.pos:], "MV") {
			p.pos += 2
			prefix = "property descriptor for "
		}
	default:
		return false
	}
	// Stack: [context, (optional y/labels), snFuncType] OR [context, (optional y/labels), ret, idx]
	if len(p.stack) < 2 {
		return false
	}
	var params []swiftTupleElem
	var retStr string
	if p.stack[len(p.stack)-1].kind == snFuncType {
		ft, _ := p.pop()
		params = ft.funcParams
		retStr = ft.funcRet
	} else {
		if len(p.stack) < 3 {
			return false
		}
		idxNode, _ := p.pop()
		retNode, _ := p.pop()
		params = extractParams(idxNode)
		retStr = renderNodeType(retNode)
	}
	labels := p.popParamLabels(len(params))
	ctxNode, ok := p.pop()
	if !ok {
		return false
	}
	labeled := applyLabels(params, labels)
	sig := formatFuncSig(labeled, retStr, false, false)
	if prefix != "" {
		p.push(swiftNode{kind: snEntity, text: fmt.Sprintf("%s%s.subscript%s", prefix, ctxNode.text, sig)})
	} else {
		p.push(swiftNode{kind: snEntity, text: fmt.Sprintf("%s.subscript%s : %s", ctxNode.text, acc, sig)})
	}
	return true
}

func (p *swiftDemangler) popQualifiedProtocol() (swiftNode, bool) {
	n, ok := p.pop()
	if !ok {
		return swiftNode{}, false
	}
	// In Swift 5 protocol lists (`_p`) and conformances (`Mc`/`TW`), a protocol's trailing `P`
	// is omitted so `[moduleIdent, protoIdent]` sits on the stack.
	if n.kind == snIdent && len(p.stack) > 0 && p.stack[len(p.stack)-1].kind == snIdent {
		mod, _ := p.pop()
		return swiftNode{kind: snNominal, text: mod.text + "." + n.text, nominalTag: 'P'}, true
	}
	return n, true
}

func (p *swiftDemangler) applyExistential() bool {
	// Collect protocol(s) back to `_` if present, otherwise single protocol on top.
	if len(p.stack) == 0 {
		return false
	}
	if p.stack[len(p.stack)-1].kind == snFirstElemMarker {
		p.pop()
		if proto, ok := p.popQualifiedProtocol(); ok {
			p.push(swiftNode{kind: snType, text: renderNodeType(proto)})
			return true
		}
		return false
	}
	// Multi-protocol composition: [P1, _, P2] + 'p'
	markIdx := -1
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].kind == snFirstElemMarker {
			markIdx = i
			break
		}
	}
	if markIdx >= 1 {
		restNodes := append([]swiftNode(nil), p.stack[markIdx+1:]...)
		p.stack = p.stack[:markIdx]
		first, ok := p.popQualifiedProtocol()
		if !ok {
			return false
		}
		parts := []string{renderNodeType(first)}
		for i := 0; i < len(restNodes); i++ {
			if restNodes[i].kind == snIdent && i+1 < len(restNodes) && restNodes[i+1].kind == snIdent {
				parts = append(parts, restNodes[i].text+"."+restNodes[i+1].text)
				i++
			} else {
				parts = append(parts, renderNodeType(restNodes[i]))
			}
		}
		p.push(swiftNode{kind: snType, text: strings.Join(parts, " & ")})
		return true
	}
	proto, ok := p.popQualifiedProtocol()
	if !ok {
		return false
	}
	p.push(swiftNode{kind: snType, text: renderNodeType(proto)})
	return true
}

func (p *swiftDemangler) parseGenericParam() bool {
	p.pos++ // skip 'q'
	if p.pos >= len(p.src) {
		return false
	}
	// `q_` = depth 0 index 1 ('B'), `q0_` = index 2 ('C'), `qd__` = depth 1 index 0 ('A1')
	if p.src[p.pos] == '_' {
		p.pos++
		p.push(swiftNode{kind: snType, text: "B"})
		return true
	}
	if p.src[p.pos] == 'd' {
		p.pos++
		p.skipIndex()
		p.skipIndex()
		p.push(swiftNode{kind: snType, text: "A1"})
		return true
	}
	if p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
		idx, ok := p.readNaturalIndex()
		if !ok {
			return false
		}
		name := genericParamLetter(idx + 2)
		p.push(swiftNode{kind: snType, text: name})
		return true
	}
	return false
}

func (p *swiftDemangler) parseGenericRequirement() bool {
	p.pos++ // skip 'R'
	if p.pos >= len(p.src) {
		return false
	}
	// `Rsz` = same-type requirement (`A == <top>`), `Rz` = same-type (`<left> == <right>`), `R<proto>` = conformance
	switch p.src[p.pos] {
	case 's':
		p.pos++
		if p.peek() == 'z' {
			p.pos++
			if top, ok := p.pop(); ok {
				p.push(swiftNode{kind: snGenericConstraint, text: "A where A == " + renderNodeType(top)})
				return true
			}
		}
		return false
	case 'z':
		p.pos++
		if len(p.stack) >= 2 {
			right, _ := p.pop()
			left, _ := p.pop()
			p.push(swiftNode{kind: snGenericConstraint, text: fmt.Sprintf("%s where %s == %s", left.text, left.text, renderNodeType(right))})
			return true
		}
		return false
	default:
		// Skip single-char constraint code if present
		p.pos++
		return true
	}
}

func (p *swiftDemangler) applyGenericSignature() {
	// Generic signature marker `l` leaves generic constraint node intact for `E` or `F`.
}

func (p *swiftDemangler) parseThunkOrSpecialization() bool {
	if p.pos+2 > len(p.src) {
		return false
	}
	p.pos++ // skip 'T'
	sub := p.src[p.pos]
	p.pos++

	wrapTop := func(prefix string) bool {
		top, ok := p.pop()
		if !ok {
			return false
		}
		p.push(swiftNode{kind: snEntity, text: prefix + top.text})
		return true
	}

	switch sub {
	case 'o':
		return wrapTop("@objc ")
	case 'O':
		return wrapTop("@objc dynamic ")
	case 'A':
		return wrapTop("partial apply forwarder for ")
	case 'R':
		return wrapTop("reabstraction thunk helper for ")
	case 'm':
		return wrapTop("merged ")
	case 'u':
		return wrapTop("async function pointer to ")
	case 'q':
		return wrapTop("method descriptor for ")
	case 'Q':
		// `TQ<n>_` = (<n+1>) await resume partial function
		idx := p.readResumeIndex()
		return wrapTop(fmt.Sprintf("(%d) await resume partial function for ", idx))
	case 'Y':
		// `TY<n>_` = (<n+1>) suspend resume partial function
		idx := p.readResumeIndex()
		return wrapTop(fmt.Sprintf("(%d) suspend resume partial function for ", idx))
	case 'W':
		// Protocol witness `TW`: stack has [conformanceType, proto (possibly [mod, name]), confMod, memberEntity]
		member, ok1 := p.pop()
		if !ok1 {
			return false
		}
		if len(p.stack) >= 3 {
			mod, _ := p.pop()
			proto, okP := p.popQualifiedProtocol()
			if !okP || len(p.stack) == 0 {
				return false
			}
			typ, _ := p.pop()
			p.push(swiftNode{
				kind: snEntity,
				text: fmt.Sprintf("protocol witness for %s in conformance %s : %s in %s", member.text, typ.text, proto.text, mod.text),
			})
			return true
		}
		p.push(swiftNode{kind: snEntity, text: "protocol witness for " + member.text})
		return true
	case 'f':
		// Function signature specialization: `Tf<pass-id><arg-specs>_<ret-spec>` (e.g. `Tf4d_n`, `Tf4g_n`, `Tf4n_n`)
		specStart := p.pos
		for p.pos < len(p.src) {
			ch := p.src[p.pos]
			if ch == 'T' && p.pos > specStart {
				break
			}
			p.pos++
		}
		return wrapTop("function signature specialization of ")
	case 'g':
		// Generic specialization `Tg5` / `Tg...`: collect specialized type args back to `_`
		for p.pos < len(p.src) && isDigit(p.src[p.pos]) {
			p.pos++
		}
		var specArgs []string
		markIdx := -1
		for i := len(p.stack) - 1; i >= 0; i-- {
			if p.stack[i].kind == snFirstElemMarker {
				markIdx = i
				break
			}
		}
		if markIdx >= 1 {
			specArgs = append(specArgs, renderNodeType(p.stack[markIdx-1]))
			for _, n := range p.stack[markIdx+1:] {
				specArgs = append(specArgs, renderNodeType(n))
			}
			p.stack = p.stack[:markIdx-1]
		}
		if len(specArgs) > 0 {
			return wrapTop(fmt.Sprintf("generic specialization <%s> of ", strings.Join(specArgs, ", ")))
		}
		return wrapTop("generic specialization of ")
	default:
		// Unknown thunk suffix: gracefully ignore trailing tag if entity is already formed.
		return len(p.stack) > 0
	}
}

func (p *swiftDemangler) parseMetadata() bool {
	if p.pos+2 > len(p.src) {
		return false
	}
	p.pos++ // skip 'M'
	sub := p.src[p.pos]
	p.pos++
	switch sub {
	case 'a':
		if top, ok := p.pop(); ok {
			p.push(swiftNode{kind: snEntity, text: "type metadata accessor for " + top.text})
			return true
		}
	case 'n':
		if top, ok := p.pop(); ok {
			p.push(swiftNode{kind: snEntity, text: "nominal type descriptor for " + top.text})
			return true
		}
	case 'm':
		if top, ok := p.pop(); ok {
			p.push(swiftNode{kind: snEntity, text: "metaclass for " + top.text})
			return true
		}
	case 'f':
		if top, ok := p.pop(); ok {
			p.push(swiftNode{kind: snEntity, text: "full type metadata for " + top.text})
			return true
		}
	case 'F':
		if top, ok := p.pop(); ok {
			p.push(swiftNode{kind: snEntity, text: "reflection metadata field descriptor " + top.text})
			return true
		}
	case 'c':
		if len(p.stack) >= 3 {
			mod, _ := p.pop()
			proto, okP := p.popQualifiedProtocol()
			if !okP || len(p.stack) == 0 {
				return false
			}
			typ, _ := p.pop()
			p.push(swiftNode{kind: snEntity, text: fmt.Sprintf("protocol conformance descriptor for %s : %s in %s", typ.text, proto.text, mod.text)})
			return true
		}
	case 'X':
		if p.peek() == 'M' {
			p.pos++
			if top, ok := p.pop(); ok {
				p.push(swiftNode{kind: snEntity, text: "module descriptor " + top.text})
				return true
			}
		}
	}
	return false
}

func (p *swiftDemangler) popParamLabels(paramCount int) []string {
	if paramCount == 0 || len(p.stack) == 0 {
		return nil
	}
	// If ALL parameters are unlabeled, Swift pushes a single `y` (snEmptyList) label list.
	if p.stack[len(p.stack)-1].kind == snEmptyList {
		p.pop()
		return nil
	}
	// Otherwise check if we have `paramCount` label nodes (snIdent or snFirstElemMarker) on top,
	// leaving at least 1 context node (or 2 for context + method name).
	if len(p.stack) < paramCount+1 {
		return nil
	}
	allLabels := true
	for i := len(p.stack) - paramCount; i < len(p.stack); i++ {
		k := p.stack[i].kind
		if k != snIdent && k != snFirstElemMarker {
			allLabels = false
			break
		}
	}
	if !allLabels {
		return nil
	}
	rawLabels := p.stack[len(p.stack)-paramCount:]
	p.stack = p.stack[:len(p.stack)-paramCount]
	out := make([]string, paramCount)
	for i, n := range rawLabels {
		if n.kind == snFirstElemMarker {
			out[i] = "_"
		} else {
			out[i] = n.text
		}
	}
	return out
}

func (p *swiftDemangler) readNaturalIndex() (int, bool) {
	start := p.pos
	for p.pos < len(p.src) && isDigit(p.src[p.pos]) {
		p.pos++
	}
	if p.pos == start || p.pos >= len(p.src) || p.src[p.pos] != '_' {
		return 0, false
	}
	n, err := strconv.Atoi(p.src[start:p.pos])
	p.pos++ // skip '_'
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func (p *swiftDemangler) readClosureIndex() int {
	if p.peek() == '_' {
		p.pos++
		return 1
	}
	if n, ok := p.readNaturalIndex(); ok {
		return n + 2
	}
	return 1
}

func (p *swiftDemangler) readResumeIndex() int {
	if p.peek() == '_' {
		p.pos++
		return 1
	}
	if n, ok := p.readNaturalIndex(); ok {
		return n + 1
	}
	return 1
}

func (p *swiftDemangler) skipIndex() {
	if p.peek() == '_' {
		p.pos++
		return
	}
	for p.pos < len(p.src) && isDigit(p.src[p.pos]) {
		p.pos++
	}
	if p.peek() == '_' {
		p.pos++
	}
}

func (p *swiftDemangler) renderTop() string {
	if len(p.stack) == 0 {
		return ""
	}
	return p.stack[len(p.stack)-1].text
}

func renderNodeType(n swiftNode) string {
	if n.kind == snEmptyList {
		return "()"
	}
	if n.kind == snTuple {
		return formatTupleElems(n.tupleElems)
	}
	if n.kind == snFuncType {
		return formatFuncSig(n.funcParams, n.funcRet, n.isAsync, n.isThrows)
	}
	return n.text
}

func extractParams(n swiftNode) []swiftTupleElem {
	switch n.kind {
	case snEmptyList:
		return nil
	case snTuple:
		out := make([]swiftTupleElem, len(n.tupleElems))
		copy(out, n.tupleElems)
		return out
	default:
		return []swiftTupleElem{{Type: renderNodeType(n)}}
	}
}

func applyLabels(params []swiftTupleElem, labels []string) []swiftTupleElem {
	if len(labels) == 0 || len(labels) != len(params) {
		return params
	}
	out := make([]swiftTupleElem, len(params))
	for i := range params {
		out[i] = swiftTupleElem{Label: labels[i], Type: params[i].Type}
	}
	return out
}

func formatTupleElems(elems []swiftTupleElem) string {
	if len(elems) == 0 {
		return "()"
	}
	parts := make([]string, len(elems))
	for i, e := range elems {
		if e.Label != "" {
			parts[i] = e.Label + ": " + e.Type
		} else {
			parts[i] = e.Type
		}
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func formatFuncSig(params []swiftTupleElem, ret string, isAsync, isThrows bool) string {
	var sb strings.Builder
	sb.WriteString(formatTupleElems(params))
	if isAsync {
		sb.WriteString(" async")
	}
	if isThrows {
		sb.WriteString(" throws")
	}
	sb.WriteString(" -> ")
	if ret == "" {
		sb.WriteString("()")
	} else {
		sb.WriteString(ret)
	}
	return sb.String()
}

func genericParamLetter(idx int) string {
	if idx >= 0 && idx < 26 {
		return string(byte('A' + idx))
	}
	return fmt.Sprintf("T%d", idx)
}
