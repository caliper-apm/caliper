package symbolicator

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// R8MethodMapping maps an obfuscated method and optional line range back to original source.
type R8MethodMapping struct {
	ObfName     string
	OrigName    string
	ObfStart    int
	ObfEnd      int
	OrigStart   int
	OrigEnd     int
	SourceFile  string
}

// R8ClassMapping maps an obfuscated class name to its original class and methods.
type R8ClassMapping struct {
	OrigClass  string
	ObfClass   string
	SourceFile string
	Methods    map[string][]R8MethodMapping
}

// R8Mapping holds a parsed ProGuard / R8 mapping.txt table.
type R8Mapping struct {
	ByObfClass map[string]*R8ClassMapping
}

// ParseR8Mapping parses standard ProGuard/R8 mapping.txt content.
func ParseR8Mapping(raw []byte) *R8Mapping {
	m := &R8Mapping{ByObfClass: make(map[string]*R8ClassMapping)}
	var cur *R8ClassMapping

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 32*1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			// Support R8 sourceFile comment: # {"id":"sourceFile","fileName":"FeedImageDecoder.kt"}
			if cur != nil && strings.Contains(trimmed, `"sourceFile"`) {
				if idx := strings.Index(trimmed, `"fileName":"`); idx >= 0 {
					rest := trimmed[idx+len(`"fileName":"`):]
					if end := strings.IndexByte(rest, '"'); end > 0 {
						cur.SourceFile = rest[:end]
					}
				}
			}
			continue
		}

		// Class header line (no leading whitespace, ends with ':')
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && strings.HasSuffix(trimmed, ":") {
			body := strings.TrimSuffix(trimmed, ":")
			parts := strings.Split(body, " -> ")
			if len(parts) == 2 {
				origClass := strings.TrimSpace(parts[0])
				obfClass := strings.TrimSpace(parts[1])
				cur = &R8ClassMapping{
					OrigClass:  origClass,
					ObfClass:   obfClass,
					SourceFile: inferJVMFile(origClass),
					Methods:    make(map[string][]R8MethodMapping),
				}
				m.ByObfClass[obfClass] = cur
			}
			continue
		}

		if cur == nil {
			continue
		}

		// Member line: [obfStart:obfEnd:]retType origMethod(args)[:origStart[:origEnd]] -> obfMethod
		arrowParts := strings.Split(trimmed, " -> ")
		if len(arrowParts) != 2 {
			continue
		}
		left := strings.TrimSpace(arrowParts[0])
		obfName := strings.TrimSpace(arrowParts[1])
		// Only parse method entries (contain parentheses).
		parenOpen := strings.IndexByte(left, '(')
		parenClose := strings.IndexByte(left, ')')
		if parenOpen < 0 || parenClose < parenOpen {
			continue
		}

		var obfStart, obfEnd, origStart, origEnd int
		afterParen := left[parenClose+1:]
		if strings.HasPrefix(afterParen, ":") {
			origRange := strings.Split(strings.TrimPrefix(afterParen, ":"), ":")
			if len(origRange) >= 1 {
				origStart, _ = strconv.Atoi(origRange[0])
				origEnd = origStart
			}
			if len(origRange) >= 2 {
				origEnd, _ = strconv.Atoi(origRange[1])
			}
		}

		beforeParen := left[:parenOpen]
		spaceIdx := strings.LastIndexByte(beforeParen, ' ')
		if spaceIdx < 0 {
			continue
		}
		origName := strings.TrimSpace(beforeParen[spaceIdx+1:])
		prefix := strings.TrimSpace(beforeParen[:spaceIdx])
		// Check leading obfStart:obfEnd:retType
		colonParts := strings.SplitN(prefix, ":", 3)
		if len(colonParts) == 3 {
			obfStart, _ = strconv.Atoi(colonParts[0])
			obfEnd, _ = strconv.Atoi(colonParts[1])
			if origStart == 0 {
				origStart = obfStart
				origEnd = obfEnd
			}
		}

		cur.Methods[obfName] = append(cur.Methods[obfName], R8MethodMapping{
			ObfName:    obfName,
			OrigName:   origName,
			ObfStart:   obfStart,
			ObfEnd:     obfEnd,
			OrigStart:  origStart,
			OrigEnd:    origEnd,
			SourceFile: cur.SourceFile,
		})
	}
	return m
}

// ResolveFrame retraces an obfuscated class.method + line into original symbol, file, and line.
func (m *R8Mapping) ResolveFrame(symbol string, obfLine int) (resolvedSymbol, resolvedFile string, resolvedLine int, ok bool) {
	if m == nil || symbol == "" {
		return "", "", 0, false
	}
	// Strip optional trailing "(SourceFile:12)" from raw JVM stack lines.
	cleanSym := symbol
	if idx := strings.IndexByte(cleanSym, '('); idx > 0 {
		if obfLine <= 0 {
			if closeIdx := strings.IndexByte(cleanSym[idx:], ')'); closeIdx > 0 {
				inside := cleanSym[idx+1 : idx+closeIdx]
				if c := strings.LastIndexByte(inside, ':'); c > 0 {
					if parsed, err := strconv.Atoi(inside[c+1:]); err == nil {
						obfLine = parsed
					}
				}
			}
		}
		cleanSym = cleanSym[:idx]
	}

	dotIdx := strings.LastIndexByte(cleanSym, '.')
	if dotIdx <= 0 || dotIdx >= len(cleanSym)-1 {
		return "", "", 0, false
	}
	obfClass := cleanSym[:dotIdx]
	obfMethod := cleanSym[dotIdx+1:]

	cls, found := m.ByObfClass[obfClass]
	if !found {
		return "", "", 0, false
	}

	candidates := cls.Methods[obfMethod]
	if len(candidates) == 0 {
		return cls.OrigClass + "." + obfMethod, cls.SourceFile, obfLine, true
	}

	// Prefer exact line-range match when obfLine is provided.
	if obfLine > 0 {
		for _, cand := range candidates {
			if cand.ObfStart > 0 && obfLine >= cand.ObfStart && obfLine <= cand.ObfEnd {
				mappedLine := cand.OrigStart
				if cand.OrigEnd > cand.OrigStart && cand.ObfEnd > cand.ObfStart {
					mappedLine = cand.OrigStart + (obfLine - cand.ObfStart)
				}
				return cls.OrigClass + "." + cand.OrigName, cand.SourceFile, mappedLine, true
			}
		}
	}

	// Fallback to first matching method overload.
	cand := candidates[0]
	mappedLine := obfLine
	if cand.OrigStart > 0 && mappedLine <= 0 {
		mappedLine = cand.OrigStart
	}
	return cls.OrigClass + "." + cand.OrigName, cand.SourceFile, mappedLine, true
}

// ResolveLine retraces a full stack line like "a.b.c.a(SourceFile:12)".
func (m *R8Mapping) ResolveLine(line string) (string, bool) {
	sym, file, ln, ok := m.ResolveFrame(strings.TrimSpace(line), 0)
	if !ok {
		return line, false
	}
	if file != "" && ln > 0 {
		return fmt.Sprintf("%s(%s:%d)", sym, file, ln), true
	}
	if file != "" {
		return fmt.Sprintf("%s(%s)", sym, file), true
	}
	return sym, true
}

func inferJVMFile(origClass string) string {
	simple := origClass
	if dot := strings.LastIndexByte(simple, '.'); dot >= 0 {
		simple = simple[dot+1:]
	}
	if dollar := strings.IndexByte(simple, '$'); dollar > 0 {
		simple = simple[:dollar]
	}
	if simple == "" {
		return ""
	}
	return simple + ".kt"
}
