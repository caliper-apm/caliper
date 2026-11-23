package symbolicator

import (
	"strconv"
	"strings"
)

// DemangleNativeSymbol demangles Itanium C++ ABI symbols (_Z... / __Z...) and strips
// Mach-O's leading underscore from plain C symbols while leaving Objective-C / Swift /
// already-readable symbols intact. Zero external dependencies.
func DemangleNativeSymbol(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// Preserve Objective-C selectors like "-[FeedVC viewDidLoad]" or "+[Cache shared]"
	if strings.HasPrefix(s, "-[") || strings.HasPrefix(s, "+[") {
		return s
	}
	// Mach-O prepends one extra "_" to C and C++ linkage names (__Z... -> _Z..., _foo -> foo).
	if strings.HasPrefix(s, "__Z") {
		s = s[1:]
	}
	if strings.HasPrefix(s, "_Z") {
		if out, ok := demangleItanium(s[2:]); ok && out != "" {
			return out
		}
		return raw
	}
	// Strip single leading underscore on Mach-O C symbols (e.g. "_decode_frame" -> "decode_frame")
	// while avoiding compiler-internal "__..." symbols.
	if len(s) > 1 && s[0] == '_' && s[1] != '_' && s[1] != '$' {
		return s[1:]
	}
	return s
}

type itaniumParser struct {
	src  string
	pos  int
	subs []string
}

func demangleItanium(body string) (string, bool) {
	p := &itaniumParser{src: body}
	name, isConst, ok := p.parseEncoding()
	if !ok || name == "" {
		return "", false
	}
	// Remaining bytes encode parameter types (if present).
	var params []string
	if p.pos < len(p.src) {
		if p.src[p.pos] == 'v' && p.pos+1 == len(p.src) {
			p.pos++
			params = []string{}
		} else {
			for p.pos < len(p.src) {
				before := p.pos
				typ, ok := p.parseType()
				if !ok || p.pos == before {
					break
				}
				if typ != "" {
					params = append(params, typ)
				}
			}
		}
		if params != nil {
			name += "(" + strings.Join(params, ", ") + ")"
		}
	}
	if isConst {
		name += " const"
	}
	return name, true
}

func (p *itaniumParser) parseEncoding() (name string, isConst bool, ok bool) {
	if p.pos >= len(p.src) {
		return "", false, false
	}
	// Nested name: N [qualifiers] <components> E
	if p.src[p.pos] == 'N' {
		p.pos++
		for p.pos < len(p.src) {
			switch p.src[p.pos] {
			case 'K':
				isConst = true
				p.pos++
				continue
			case 'V', 'r':
				p.pos++
				continue
			}
			break
		}
		var parts []string
		for p.pos < len(p.src) && p.src[p.pos] != 'E' {
			before := p.pos
			part, ok := p.parseNestedComponent(parts)
			if !ok || p.pos == before {
				return "", false, false
			}
			if part != "" {
				parts = append(parts, part)
				// Record prefix substitution per Itanium ABI (except final component handled lazily).
				p.addSub(strings.Join(parts, "::"))
			}
		}
		if p.pos < len(p.src) && p.src[p.pos] == 'E' {
			p.pos++
		}
		return strings.Join(parts, "::"), isConst, len(parts) > 0
	}
	// Standard std:: prefix: St <unqualified-name>
	if strings.HasPrefix(p.src[p.pos:], "St") {
		p.pos += 2
		comp, ok := p.parseUnqualified(nil)
		if !ok {
			return "", false, false
		}
		return "std::" + comp, false, true
	}
	// Unscoped top-level function: <len><ident> [I <template-args> E]
	comp, ok := p.parseUnqualified(nil)
	if !ok {
		return "", false, false
	}
	if p.pos < len(p.src) && p.src[p.pos] == 'I' {
		if targs, ok := p.parseTemplateArgs(); ok {
			comp += targs
		}
	}
	return comp, false, true
}

func (p *itaniumParser) parseNestedComponent(prev []string) (string, bool) {
	if p.pos >= len(p.src) {
		return "", false
	}
	ch := p.src[p.pos]
	// Template arguments apply to the previous component.
	if ch == 'I' {
		targs, ok := p.parseTemplateArgs()
		if !ok {
			return "", false
		}
		if len(prev) > 0 {
			prev[len(prev)-1] += targs
			p.addSub(strings.Join(prev, "::"))
		}
		return "", true
	}
	if ch == 'S' {
		sub, ok := p.parseSubstitution()
		if !ok {
			return "", false
		}
		return sub, true
	}
	return p.parseUnqualified(prev)
}

func (p *itaniumParser) parseUnqualified(prev []string) (string, bool) {
	if p.pos >= len(p.src) {
		return "", false
	}
	// Constructors C1/C2/C3 & Destructors D0/D1/D2
	if p.pos+2 <= len(p.src) {
		two := p.src[p.pos : p.pos+2]
		if two == "C1" || two == "C2" || two == "C3" {
			p.pos += 2
			if len(prev) > 0 {
				return stripTemplate(prev[len(prev)-1]), true
			}
			return "ctor", true
		}
		if two == "D0" || two == "D1" || two == "D2" {
			p.pos += 2
			if len(prev) > 0 {
				return "~" + stripTemplate(prev[len(prev)-1]), true
			}
			return "~dtor", true
		}
	}
	// Length-prefixed source name
	if p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
		return p.parseSourceName()
	}
	// Common operator names
	if p.pos+2 <= len(p.src) {
		if op, ok := itaniumOperators[p.src[p.pos:p.pos+2]]; ok {
			p.pos += 2
			return op, true
		}
	}
	return "", false
}

func (p *itaniumParser) parseSourceName() (string, bool) {
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
	ident := p.src[p.pos : p.pos+n]
	p.pos += n
	// Handle ABI tag B<len><tag> if immediately attached.
	for strings.HasPrefix(p.src[p.pos:], "B") {
		save := p.pos
		p.pos++
		if _, ok := p.parseSourceName(); !ok {
			p.pos = save
			break
		}
	}
	return ident, true
}

func (p *itaniumParser) parseTemplateArgs() (string, bool) {
	if p.pos >= len(p.src) || p.src[p.pos] != 'I' {
		return "", false
	}
	p.pos++
	var args []string
	for p.pos < len(p.src) && p.src[p.pos] != 'E' {
		before := p.pos
		if p.src[p.pos] == 'L' {
			// Literal template arg: L <type> <value> E
			p.pos++
			_, _ = p.parseType()
			valStart := p.pos
			for p.pos < len(p.src) && p.src[p.pos] != 'E' {
				p.pos++
			}
			val := p.src[valStart:p.pos]
			if p.pos < len(p.src) {
				p.pos++
			}
			args = append(args, val)
			continue
		}
		typ, ok := p.parseType()
		if !ok || p.pos == before {
			return "", false
		}
		args = append(args, typ)
	}
	if p.pos < len(p.src) && p.src[p.pos] == 'E' {
		p.pos++
	}
	return "<" + strings.Join(args, ", ") + ">", true
}

func (p *itaniumParser) parseType() (string, bool) {
	if p.pos >= len(p.src) {
		return "", false
	}
	switch p.src[p.pos] {
	case 'P':
		p.pos++
		inner, ok := p.parseType()
		if !ok {
			return "", false
		}
		t := inner + "*"
		p.addSub(t)
		return t, true
	case 'R':
		p.pos++
		inner, ok := p.parseType()
		if !ok {
			return "", false
		}
		t := inner + "&"
		p.addSub(t)
		return t, true
	case 'O':
		p.pos++
		inner, ok := p.parseType()
		if !ok {
			return "", false
		}
		t := inner + "&&"
		p.addSub(t)
		return t, true
	case 'K':
		p.pos++
		inner, ok := p.parseType()
		if !ok {
			return "", false
		}
		t := "const " + inner
		p.addSub(t)
		return t, true
	case 'V':
		p.pos++
		inner, ok := p.parseType()
		if !ok {
			return "", false
		}
		return "volatile " + inner, true
	case 'S':
		return p.parseSubstitution()
	case 'N':
		name, _, ok := p.parseEncoding()
		return name, ok
	}
	if p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
		name, ok := p.parseSourceName()
		if !ok {
			return "", false
		}
		if p.pos < len(p.src) && p.src[p.pos] == 'I' {
			if targs, ok := p.parseTemplateArgs(); ok {
				name += targs
			}
		}
		p.addSub(name)
		return name, true
	}
	if builtin, ok := itaniumBuiltins[p.src[p.pos]]; ok {
		p.pos++
		return builtin, true
	}
	return "", false
}

func (p *itaniumParser) parseSubstitution() (string, bool) {
	if p.pos+2 > len(p.src) || p.src[p.pos] != 'S' {
		return "", false
	}
	switch p.src[p.pos+1] {
	case 't':
		p.pos += 2
		return "std", true
	case 'a':
		p.pos += 2
		return "std::allocator", true
	case 'b':
		p.pos += 2
		return "std::basic_string", true
	case 's':
		p.pos += 2
		return "std::string", true
	case 'i':
		p.pos += 2
		return "std::istream", true
	case 'o':
		p.pos += 2
		return "std::ostream", true
	case 'd':
		p.pos += 2
		return "std::iostream", true
	case '_':
		p.pos += 2
		if len(p.subs) > 0 {
			return p.subs[0], true
		}
		return "S_", true
	}
	// S <base-36 seq-id> _
	p.pos++
	start := p.pos
	for p.pos < len(p.src) && p.src[p.pos] != '_' {
		p.pos++
	}
	if p.pos >= len(p.src) {
		return "", false
	}
	seqStr := p.src[start:p.pos]
	p.pos++ // skip '_'
	idx, err := strconv.ParseInt(seqStr, 36, 32)
	if err == nil && int(idx+1) < len(p.subs) {
		return p.subs[idx+1], true
	}
	return "S" + seqStr + "_", true
}

func (p *itaniumParser) addSub(s string) {
	if s == "" {
		return
	}
	for _, existing := range p.subs {
		if existing == s {
			return
		}
	}
	p.subs = append(p.subs, s)
}

func stripTemplate(s string) string {
	if idx := strings.IndexByte(s, '<'); idx >= 0 {
		return s[:idx]
	}
	return s
}

var itaniumBuiltins = map[byte]string{
	'v': "void",
	'w': "wchar_t",
	'b': "bool",
	'c': "char",
	'a': "signed char",
	'h': "unsigned char",
	's': "short",
	't': "unsigned short",
	'i': "int",
	'j': "unsigned int",
	'l': "long",
	'm': "unsigned long",
	'x': "long long",
	'y': "unsigned long long",
	'f': "float",
	'd': "double",
	'e': "long double",
	'z': "...",
}

var itaniumOperators = map[string]string{
	"nw": "operator new",
	"na": "operator new[]",
	"dl": "operator delete",
	"da": "operator delete[]",
	"eq": "operator==",
	"ne": "operator!=",
	"lt": "operator<",
	"gt": "operator>",
	"le": "operator<=",
	"ge": "operator>=",
	"aS": "operator=",
	"cl": "operator()",
	"ix": "operator[]",
	"pt": "operator->",
	"pl": "operator+",
	"mi": "operator-",
}
