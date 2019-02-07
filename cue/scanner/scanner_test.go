// Copyright 2018 The CUE Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package scanner

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/token"
	"github.com/google/go-cmp/cmp"
)

var fset = token.NewFileSet()

const /* class */ (
	special = iota
	literal
	operator
	keyword
)

func tokenclass(tok token.Token) int {
	switch {
	case tok.IsLiteral():
		return literal
	case tok.IsOperator():
		return operator
	case tok.IsKeyword():
		return keyword
	}
	return special
}

type elt struct {
	tok   token.Token
	lit   string
	class int
}

var testTokens = [...]elt{
	// Special tokens
	{token.COMMENT, "/* a comment */", special},
	{token.COMMENT, "// a comment \n", special},
	{token.COMMENT, "/*\r*/", special},
	{token.COMMENT, "//\r\n", special},

	// Identifiers and basic type literals
	{token.BOTTOM, "_|_", literal},
	{token.BOTTOM, "_|_", literal},

	{token.IDENT, "foobar", literal},
	{token.IDENT, "a۰۱۸", literal},
	{token.IDENT, "foo६४", literal},
	{token.IDENT, "bar９８７６", literal},
	{token.IDENT, "ŝ", literal},
	{token.IDENT, "ŝfoo", literal},
	{token.INT, "0", literal},
	{token.INT, "1", literal},
	{token.INT, "123456789012345678890", literal},
	{token.INT, "12345_67890_12345_6788_90", literal},
	{token.INT, "1234567M", literal},
	{token.INT, "1234567Mi", literal},
	{token.INT, "1234567", literal},
	{token.INT, ".3Mi", literal},
	{token.INT, "3.3Mi", literal},
	{token.INT, "0xcafebabe", literal},
	{token.INT, "0b1100_1001", literal},
	{token.INT, "0o1234567", literal},
	{token.FLOAT, "0.", literal},
	{token.FLOAT, ".0", literal},
	{token.FLOAT, "3.14159265", literal},
	{token.FLOAT, "1e0", literal},
	{token.FLOAT, "1e+100", literal},
	{token.FLOAT, "1e-100", literal},
	{token.FLOAT, "2.71828e-1000", literal},
	{token.STRING, "`aa\n\n`", literal},
	{token.STRING, "'a'", literal},
	{token.STRING, "'\\000'", literal},
	{token.STRING, "'\\xFF'", literal},
	{token.STRING, "'\\uff16'", literal},
	{token.STRING, "'\\U0000ff16'", literal},
	{token.STRING, "'foobar'", literal},
	{token.STRING, "`" + `foo
	                        bar` +
		"`",
		literal,
	},
	{token.STRING, "`foobar`", literal},
	{token.STRING, "`\r`", literal},
	{token.STRING, "`foo\r\nbar`", literal},
	{token.STRING, "'" + `\r` + "'", literal},
	{token.STRING, "'foo" + `\r\n` + "bar'", literal},
	{token.STRING, `"foobar"`, literal},
	{token.STRING, `"""\n  foobar\n  """`, literal},

	// Operators and delimiters
	{token.ADD, "+", operator},
	{token.SUB, "-", operator},
	{token.MUL, "*", operator},
	{token.QUO, "/", operator},
	{token.REM, "%", operator},

	{token.UNIFY, "&", operator},
	{token.DISJUNCTION, "|", operator},

	{token.LAND, "&&", operator},
	{token.LOR, "||", operator},

	{token.EQL, "==", operator},
	{token.LSS, "<", operator},
	{token.GTR, ">", operator},
	{token.BIND, "=", operator},
	{token.NOT, "!", operator},

	{token.NEQ, "!=", operator},
	{token.LEQ, "<=", operator},
	{token.GEQ, ">=", operator},
	{token.ELLIPSIS, "...", operator},

	{token.LPAREN, "(", operator},
	{token.LBRACK, "[", operator},
	{token.LBRACE, "{", operator},
	{token.COMMA, ",", operator},
	{token.PERIOD, ".", operator},

	{token.RPAREN, ")", operator},
	{token.RBRACK, "]", operator},
	{token.RBRACE, "}", operator},
	{token.COLON, ":", operator},

	// Keywords
	{token.TRUE, "true", keyword},
	{token.FALSE, "false", keyword},
	{token.NULL, "null", keyword},

	{token.FOR, "for", keyword},
	{token.IF, "if", keyword},
	{token.IN, "in", keyword},
}

const whitespace = "  \t  \n\n\n" // to separate tokens

var source = func() []byte {
	var src []byte
	for _, t := range testTokens {
		src = append(src, t.lit...)
		src = append(src, whitespace...)
	}
	return src
}()

func newlineCount(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			n++
		}
	}
	return n
}

func checkPosScan(t *testing.T, lit string, p token.Pos, expected token.Position) {
	pos := fset.Position(p)
	if pos.Filename != expected.Filename {
		t.Errorf("bad filename for %q: got %s, expected %s", lit, pos.Filename, expected.Filename)
	}
	if pos.Offset != expected.Offset {
		t.Errorf("bad position for %q: got %d, expected %d", lit, pos.Offset, expected.Offset)
	}
	if pos.Line != expected.Line {
		t.Errorf("bad line for %q: got %d, expected %d", lit, pos.Line, expected.Line)
	}
	if pos.Column != expected.Column {
		t.Errorf("bad column for %q: got %d, expected %d", lit, pos.Column, expected.Column)
	}
}

// Verify that calling Scan() provides the correct results.
func TestScan(t *testing.T) {
	whitespace_linecount := newlineCount(whitespace)

	// error handler
	eh := func(_ token.Position, msg string) {
		t.Errorf("error handler called (msg = %s)", msg)
	}

	// verify scan
	var s Scanner
	s.Init(fset.AddFile("", fset.Base(), len(source)), source, eh, ScanComments|dontInsertCommas)

	// set up expected position
	epos := token.Position{
		Filename: "",
		Offset:   0,
		Line:     1,
		Column:   1,
	}

	index := 0
	for {
		pos, tok, lit := s.Scan()

		// check position
		if tok == token.EOF {
			// correction for EOF
			epos.Line = newlineCount(string(source))
			epos.Column = 2
		}
		checkPosScan(t, lit, pos, epos)

		// check token
		e := elt{token.EOF, "", special}
		if index < len(testTokens) {
			e = testTokens[index]
			index++
		}
		if tok != e.tok {
			t.Errorf("bad token for %q: got %s, expected %s", lit, tok, e.tok)
		}

		// check token class
		if tokenclass(tok) != e.class {
			t.Errorf("bad class for %q: got %d, expected %d", lit, tokenclass(tok), e.class)
		}

		// check literal
		elit := ""
		switch e.tok {
		case token.COMMENT:
			// no CRs in comments
			elit = string(stripCR([]byte(e.lit)))
			//-style comment literal doesn't contain newline
			if elit[1] == '/' {
				elit = elit[0 : len(elit)-1]
			}
		case token.IDENT:
			elit = e.lit
		case token.COMMA:
			elit = ","
		default:
			if e.tok.IsLiteral() {
				// no CRs in raw string literals
				elit = e.lit
				if elit[0] == '`' {
					elit = string(stripCR([]byte(elit)))
				}
			} else if e.tok.IsKeyword() {
				elit = e.lit
			}
		}
		if lit != elit {
			t.Errorf("bad literal for %q: got %q, expected %q", lit, lit, elit)
		}

		if tok == token.EOF {
			break
		}

		// update position
		epos.Offset += len(e.lit) + len(whitespace)
		epos.Line += newlineCount(e.lit) + whitespace_linecount

	}

	if s.ErrorCount != 0 {
		t.Errorf("found %d errors", s.ErrorCount)
	}
}

func checkComma(t *testing.T, line string, mode Mode) {
	var S Scanner
	file := fset.AddFile("TestCommas", fset.Base(), len(line))
	S.Init(file, []byte(line), nil, mode)
	pos, tok, lit := S.Scan()
	for tok != token.EOF {
		if tok == token.ILLEGAL {
			// the illegal token literal indicates what
			// kind of semicolon literal to expect
			commaLit := "\n"
			if lit[0] == '#' {
				commaLit = ","
			}
			// next token must be a comma
			commaPos := file.Position(pos)
			commaPos.Offset++
			commaPos.Column++
			pos, tok, lit = S.Scan()
			if tok == token.COMMA {
				if lit != commaLit {
					t.Errorf(`bad literal for %q: got %q (%q), expected %q`, line, lit, tok, commaLit)
				}
				checkPosScan(t, line, pos, commaPos)
			} else {
				t.Errorf("bad token for %q: got %s, expected ','", line, tok)
			}
		} else if tok == token.COMMA {
			t.Errorf("bad token for %q: got ',', expected no ','", line)
		}
		pos, tok, lit = S.Scan()
	}
}

var lines = []string{
	// # indicates a comma present in the source
	// ? indicates an automatically inserted comma
	"",
	"\ufeff#,", // first BOM is ignored
	"#,",
	"foo?\n",
	"_foo?\n",
	"123?\n",
	"1.2?\n",
	"'x'?\n",
	"_|_?\n",
	"_|_?\n",
	`"x"` + "?\n",
	"`x`?\n",
	`"""
		foo
		"""` + "?\n",
	// `"""
	// 	foo \(bar)
	// 	"""` + "?\n",
	`'''
		foo
		'''` + "?\n",

	"+\n",
	"-\n",
	"*\n",
	"/\n",
	"%\n",

	"&\n",
	// "&?\n",
	"|\n",

	"&&\n",
	"||\n",
	"<-\n",
	"->\n",

	"==\n",
	"<\n",
	">\n",
	"=\n",
	"!\n",

	"!=\n",
	"<=\n",
	">=\n",
	":=\n",
	"...\n",

	"(\n",
	"[\n",
	"[[\n",
	"{\n",
	"{{\n",
	"#,\n",
	".\n",

	")?\n",
	"]?\n",
	"]]?\n",
	"}?\n",
	"}}?\n",
	":\n",
	";?\n",

	"true?\n",
	"false?\n",
	"null?\n",

	"foo?//comment\n",
	"foo?//comment",
	"foo?/*comment*/\n",
	"foo?/*\n*/",
	"foo?/*comment*/    \n",
	"foo?/*\n*/    ",

	"foo    ?// comment\n",
	"foo    ?// comment",
	"foo    ?/*comment*/\n",
	"foo    ?/*\n*/",
	"foo    ?/*  */ /* \n */ bar?/**/\n",
	"foo    ?/*0*/ /*1*/ /*2*/\n",

	"foo    ?/*comment*/    \n",
	"foo    ?/*0*/ /*1*/ /*2*/    \n",
	"foo	?/**/ /*-------------*/       /*----\n*/bar       ?/*  \n*/baa?\n",
	"foo    ?/* an EOF terminates a line */",
	"foo    ?/* an EOF terminates a line */ /*",
	"foo    ?/* an EOF terminates a line */ //",

	// "package main?\n\nfunc main() {\n\tif {\n\t\treturn /* */ }?\n}?\n",
	// "package main?",
}

func TestCommas(t *testing.T) {
	for _, line := range lines {
		checkComma(t, line, 0)
		checkComma(t, line, ScanComments)

		// if the input ended in newlines, the input must tokenize the
		// same with or without those newlines
		for i := len(line) - 1; i >= 0 && line[i] == '\n'; i-- {
			checkComma(t, line[0:i], 0)
			checkComma(t, line[0:i], ScanComments)
		}
	}
}

func TestRelative(t *testing.T) {
	test := `
	package foo

	// comment
	a: /* a */1
	b :    5 /*
	   line one
	   line two
	*/
	c: "dfs"
	`
	want := []string{
		`newline IDENT    package`,
		`blank   IDENT    foo`,
		"elided  ,        \n",
		`section COMMENT  // comment`,
		`newline IDENT    a`,
		`nospace :        `,
		`blank   COMMENT  /* a */`,
		`nospace INT      1`,
		"elided  ,        \n",
		`newline IDENT    b`,
		`blank   :        `,
		`blank   INT      5`,
		"elided  ,        \n",
		"blank   COMMENT  /*\n\t   line one\n\t   line two\n\t*/",
		`newline IDENT    c`,
		`nospace :        `,
		`blank   STRING   "dfs"`,
		"elided  ,        \n",
	}
	var S Scanner
	f := fset.AddFile("TestCommas", fset.Base(), len(test))
	S.Init(f, []byte(test), nil, ScanComments)
	pos, tok, lit := S.Scan()
	got := []string{}
	for tok != token.EOF {
		got = append(got, fmt.Sprintf("%-7s %-8s %s", pos.RelPos(), tok, lit))
		pos, tok, lit = S.Scan()
	}
	if !cmp.Equal(got, want) {
		t.Error(cmp.Diff(got, want))
	}
}

type segment struct {
	srcline  string // a line of source text
	filename string // filename for current token
	line     int    // line number for current token
}

var segments = []segment{
	// exactly one token per line since the test consumes one token per segment
	{"  line1", filepath.Join("dir", "TestLineComments"), 1},
	{"\nline2", filepath.Join("dir", "TestLineComments"), 2},
	{"\nline3  //line File1.go:100", filepath.Join("dir", "TestLineComments"), 3}, // bad line comment, ignored
	{"\nline4", filepath.Join("dir", "TestLineComments"), 4},
	{"\n//line File1.go:100\n  line100", filepath.Join("dir", "File1.go"), 100},
	{"\n//line  \t :42\n  line1", "", 42},
	{"\n//line File2.go:200\n  line200", filepath.Join("dir", "File2.go"), 200},
	{"\n//line foo\t:42\n  line42", filepath.Join("dir", "foo"), 42},
	{"\n //line foo:42\n  line44", filepath.Join("dir", "foo"), 44},           // bad line comment, ignored
	{"\n//line foo 42\n  line46", filepath.Join("dir", "foo"), 46},            // bad line comment, ignored
	{"\n//line foo:42 extra text\n  line48", filepath.Join("dir", "foo"), 48}, // bad line comment, ignored
	{"\n//line ./foo:42\n  line42", filepath.Join("dir", "foo"), 42},
	{"\n//line a/b/c/File1.go:100\n  line100", filepath.Join("dir", "a", "b", "c", "File1.go"), 100},
}

var unixsegments = []segment{
	{"\n//line /bar:42\n  line42", "/bar", 42},
}

var winsegments = []segment{
	{"\n//line c:\\bar:42\n  line42", "c:\\bar", 42},
	{"\n//line c:\\dir\\File1.go:100\n  line100", "c:\\dir\\File1.go", 100},
}

// Verify that comments of the form "//line filename:line" are interpreted correctly.
func TestLineComments(t *testing.T) {
	segs := segments
	if runtime.GOOS == "windows" {
		segs = append(segs, winsegments...)
	} else {
		segs = append(segs, unixsegments...)
	}

	// make source
	var src string
	for _, e := range segs {
		src += e.srcline
	}

	// verify scan
	var S Scanner
	f := fset.AddFile(filepath.Join("dir", "TestLineComments"), fset.Base(), len(src))
	S.Init(f, []byte(src), nil, dontInsertCommas)
	for _, s := range segs {
		p, _, lit := S.Scan()
		pos := f.Position(p)
		checkPosScan(t, lit, p, token.Position{
			Filename: s.filename,
			Offset:   pos.Offset,
			Line:     s.line,
			Column:   pos.Column,
		})
	}

	if S.ErrorCount != 0 {
		t.Errorf("found %d errors", S.ErrorCount)
	}
}

// Verify that initializing the same scanner more than once works correctly.
func TestInit(t *testing.T) {
	var s Scanner

	// 1st init
	src1 := "false true { }"
	f1 := fset.AddFile("src1", fset.Base(), len(src1))
	s.Init(f1, []byte(src1), nil, dontInsertCommas)
	if f1.Size() != len(src1) {
		t.Errorf("bad file size: got %d, expected %d", f1.Size(), len(src1))
	}
	s.Scan()              // false
	s.Scan()              // true
	_, tok, _ := s.Scan() // {
	if tok != token.LBRACE {
		t.Errorf("bad token: got %s, expected %s", tok, token.LBRACE)
	}

	// 2nd init
	src2 := "null true { ]"
	f2 := fset.AddFile("src2", fset.Base(), len(src2))
	s.Init(f2, []byte(src2), nil, dontInsertCommas)
	if f2.Size() != len(src2) {
		t.Errorf("bad file size: got %d, expected %d", f2.Size(), len(src2))
	}
	_, tok, _ = s.Scan() // go
	if tok != token.NULL {
		t.Errorf("bad token: got %s, expected %s", tok, token.NULL)
	}

	if s.ErrorCount != 0 {
		t.Errorf("found %d errors", s.ErrorCount)
	}
}

func TestScanTemplate(t *testing.T) {
	// error handler
	eh := func(pos token.Position, msg string) {
		t.Errorf("error handler called (pos = %v, msg = %s)", pos, msg)
	}
	trim := func(s string) string { return strings.Trim(s, `"\\()`) }

	sources := []string{
		`"first\(first)\\second\(second)"`,
		`"level\( ["foo", "level", level ][2] )end\( end )"`,
		`"level\( { "foo": 1, "bar": level } )end\(end)"`,
	}
	for i, src := range sources {
		name := fmt.Sprintf("tsrc%d", i)
		t.Run(name, func(t *testing.T) {
			f := fset.AddFile(name, fset.Base(), len(src))

			// verify scan
			var s Scanner
			s.Init(f, []byte(src), eh, ScanComments)

			count := 0
			var lit, str string
			for tok := token.ILLEGAL; tok != token.EOF; {
				switch tok {
				case token.LPAREN:
					count++
				case token.RPAREN:
					if count--; count == 0 {
						str = trim(s.ResumeInterpolation('"', 1))
					}
				case token.INTERPOLATION:
					str = trim(lit)
				case token.IDENT:
					if lit != str {
						t.Errorf("str: got %v; want %v", lit, str)
					}
				}
				_, tok, lit = s.Scan()
			}
		})
	}
}

func TestStdErrorHander(t *testing.T) {
	const src = "#\n" + // illegal character, cause an error
		"# #\n" + // two errors on the same line
		"//line File2:20\n" +
		"#\n" + // different file, but same line
		"//line File2:1\n" +
		"# #\n" + // same file, decreasing line number
		"//line File1:1\n" +
		"# # #" // original file, line 1 again

	var list errors.List
	eh := func(pos token.Position, msg string) { list.AddNew(pos, msg) }

	var s Scanner
	s.Init(fset.AddFile("File1", fset.Base(), len(src)), []byte(src), eh, dontInsertCommas)
	for {
		if _, tok, _ := s.Scan(); tok == token.EOF {
			break
		}
	}

	if len(list) != s.ErrorCount {
		t.Errorf("found %d errors, expected %d", len(list), s.ErrorCount)
	}

	if len(list) != 9 {
		t.Errorf("found %d raw errors, expected 9", len(list))
		errors.Print(os.Stderr, list)
	}

	list.Sort()
	if len(list) != 9 {
		t.Errorf("found %d sorted errors, expected 9", len(list))
		errors.Print(os.Stderr, list)
	}

	list.RemoveMultiples()
	if len(list) != 4 {
		t.Errorf("found %d one-per-line errors, expected 4", len(list))
		errors.Print(os.Stderr, list)
	}
}

type errorCollector struct {
	cnt int            // number of errors encountered
	msg string         // last error message encountered
	pos token.Position // last error position encountered
}

func checkError(t *testing.T, src string, tok token.Token, pos int, lit, err string) {
	t.Helper()
	var s Scanner
	var h errorCollector
	eh := func(pos token.Position, msg string) {
		h.cnt++
		h.msg = msg
		h.pos = pos
	}
	s.Init(fset.AddFile("", fset.Base(), len(src)), []byte(src), eh, ScanComments|dontInsertCommas)
	_, tok0, lit0 := s.Scan()
	if tok0 != tok {
		t.Errorf("%q: got %s, expected %s", src, tok0, tok)
	}
	if tok0 != token.ILLEGAL && lit0 != lit {
		t.Errorf("%q: got literal %q, expected %q", src, lit0, lit)
	}
	cnt := 0
	if err != "" {
		cnt = 1
	}
	if h.cnt != cnt {
		t.Errorf("%q: got cnt %d, expected %d", src, h.cnt, cnt)
	}
	if h.msg != err {
		t.Errorf("%q: got msg %q, expected %q", src, h.msg, err)
	}
	if h.pos.Offset != pos {
		t.Errorf("%q: got offset %d, expected %d", src, h.pos.Offset, pos)
	}
}

var errorTests = []struct {
	src string
	tok token.Token
	pos int
	lit string
	err string
}{
	{"\a", token.ILLEGAL, 0, "", "illegal character U+0007"},
	{`?`, token.ILLEGAL, 0, "", "illegal character U+003F '?'"},
	{`…`, token.ILLEGAL, 0, "", "illegal character U+2026 '…'"},
	{`_|`, token.ILLEGAL, 0, "", "illegal token '_|'; expected '_'"},
	// {`' '`, STRING, 0, `' '`, ""},
	// {"`\0`", STRING, 3, `'\0'`, "illegal character U+0027 ''' in escape sequence"},
	// {`'\07'`, STRING, 4, `'\07'`, "illegal character U+0027 ''' in escape sequence"},
	{`"\8"`, token.STRING, 2, `"\8"`, "unknown escape sequence"},
	{`"\08"`, token.STRING, 3, `"\08"`, "illegal character U+0038 '8' in escape sequence"},
	{`"\x"`, token.STRING, 3, `"\x"`, "illegal character U+0022 '\"' in escape sequence"},
	{`"\x0"`, token.STRING, 4, `"\x0"`, "illegal character U+0022 '\"' in escape sequence"},
	{`"\x0g"`, token.STRING, 4, `"\x0g"`, "illegal character U+0067 'g' in escape sequence"},
	{`"\u"`, token.STRING, 3, `"\u"`, "illegal character U+0022 '\"' in escape sequence"},
	{`"\u0"`, token.STRING, 4, `"\u0"`, "illegal character U+0022 '\"' in escape sequence"},
	{`"\u00"`, token.STRING, 5, `"\u00"`, "illegal character U+0022 '\"' in escape sequence"},
	{`"\u000"`, token.STRING, 6, `"\u000"`, "illegal character U+0022 '\"' in escape sequence"},
	// {`"\u000`, token.STRING, 6, `"\u000`, "string literal not terminated"}, two errors
	{`"\u0000"`, token.STRING, 0, `"\u0000"`, ""},
	{`"\U"`, token.STRING, 3, `"\U"`, "illegal character U+0022 '\"' in escape sequence"},
	{`"\U0"`, token.STRING, 4, `"\U0"`, "illegal character U+0022 '\"' in escape sequence"},
	{`"\U00"`, token.STRING, 5, `"\U00"`, "illegal character U+0022 '\"' in escape sequence"},
	{`"\U000"`, token.STRING, 6, `"\U000"`, "illegal character U+0022 '\"' in escape sequence"},
	{`"\U0000"`, token.STRING, 7, `"\U0000"`, "illegal character U+0022 '\"' in escape sequence"},
	{`"\U00000"`, token.STRING, 8, `"\U00000"`, "illegal character U+0022 '\"' in escape sequence"},
	{`"\U000000"`, token.STRING, 9, `"\U000000"`, "illegal character U+0022 '\"' in escape sequence"},
	{`"\U0000000"`, token.STRING, 10, `"\U0000000"`, "illegal character U+0022 '\"' in escape sequence"},
	// {`"\U0000000`, token.STRING, 10, `"\U0000000`, "string literal not terminated"}, // escape sequence not terminated"}, two errors
	{`"\U00000000"`, token.STRING, 0, `"\U00000000"`, ""},
	{`"\Uffffffff"`, token.STRING, 2, `"\Uffffffff"`, "escape sequence is invalid Unicode code point"},
	{`'`, token.STRING, 0, `'`, "string literal not terminated"},
	// TODO
	// {`'\`, token.STRING, 0, `'\`, "raw string literal not terminated"}, // "escape sequence not terminated"},
	// {"`\n", token.STRING, 0, s"`\n", "raw string literal not terminated"},
	// {"'\n   ", token.STRING, 0, "'", "raw string literal not terminated"},
	{`""`, token.STRING, 0, `""`, ""},
	{`"abc`, token.STRING, 0, `"abc`, "string literal not terminated"},
	{`""abc`, token.STRING, 0, `""`, ""},
	{`"""abc`, token.STRING, 0, `"""abc`, "string literal not terminated"},
	{`'''abc`, token.STRING, 0, `'''abc`, "string literal not terminated"},
	{"\"abc\n", token.STRING, 0, `"abc`, "string literal not terminated"},
	{"\"abc\n   ", token.STRING, 0, `"abc`, "string literal not terminated"},
	{"``", token.STRING, 0, "``", ""},
	// {"$", IDENT, 0, "$", ""}, // TODO: for root of file?
	{"`", token.STRING, 0, "`", "raw string literal not terminated"},
	{"''", token.STRING, 0, "''", ""},
	{"'", token.STRING, 0, "'", "string literal not terminated"},
	{"/**/", token.COMMENT, 0, "/**/", ""},
	{"/*", token.COMMENT, 0, "/*", "comment not terminated"},
	{"0", token.INT, 0, "0", ""},
	{"077", token.INT, 0, "077", "illegal integer number"},
	{"078.", token.FLOAT, 0, "078.", ""},
	{"07801234567.", token.FLOAT, 0, "07801234567.", ""},
	{"078e0", token.FLOAT, 0, "078e0", ""},
	{"078", token.INT, 0, "078", "illegal integer number"},
	{"07800000009", token.INT, 0, "07800000009", "illegal integer number"},
	{"0x", token.INT, 0, "0x", "illegal hexadecimal number"},
	{"0X", token.INT, 0, "0X", "illegal hexadecimal number"},
	{"0Xbeef_", token.INT, 6, "0Xbeef_", "illegal '_' in number"},
	{"0b", token.INT, 0, "0b", "illegal binary number"},
	{"0o", token.INT, 0, "0o", "illegal octal number"},
	// {"123456789012345678890_i", IMAG, 21, "123456789012345678890_i", "illegal '_' in number"},
	{"\"abc\x00def\"", token.STRING, 4, "\"abc\x00def\"", "illegal character NUL"},
	{"\"abc\x80def\"", token.STRING, 4, "\"abc\x80def\"", "illegal UTF-8 encoding"},
	{"\ufeff\ufeff", token.ILLEGAL, 3, "\ufeff\ufeff", "illegal byte order mark"}, // only first BOM is ignored
	{"//\ufeff", token.COMMENT, 2, "//\ufeff", "illegal byte order mark"},         // only first BOM is ignored
	// {"`a\ufeff`", IDENT, 2, "`a\ufeff`", "illegal byte order mark"},                                // only first BOM is ignored
	{`"` + "abc\ufeffdef" + `"`, token.STRING, 4, `"` + "abc\ufeffdef" + `"`, "illegal byte order mark"}, // only first BOM is ignored
}

func TestScanErrors(t *testing.T) {
	for _, e := range errorTests {
		t.Run(e.src, func(t *testing.T) {
			checkError(t, e.src, e.tok, e.pos, e.lit, e.err)
		})
	}
}

// Verify that no comments show up as literal values when skipping comments.
func TestNoLiteralComments(t *testing.T) {
	var src = `
		a: {
			A: 1 // foo
		}

		b: {
			B: 2
			// foo
		}

		c: 3 // foo

		d: 4
		// foo

		b anycode(): {
		// foo
		}
	`
	var s Scanner
	s.Init(fset.AddFile("", fset.Base(), len(src)), []byte(src), nil, 0)
	for {
		pos, tok, lit := s.Scan()
		class := tokenclass(tok)
		if lit != "" && class != keyword && class != literal && tok != token.COMMA {
			t.Errorf("%s: tok = %s, lit = %q", fset.Position(pos), tok, lit)
		}
		if tok <= token.EOF {
			break
		}
	}
}

func BenchmarkScan(b *testing.B) {
	b.StopTimer()
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(source))
	var s Scanner
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		s.Init(file, source, nil, ScanComments)
		for {
			_, tok, _ := s.Scan()
			if tok == token.EOF {
				break
			}
		}
	}
}

func BenchmarkScanFile(b *testing.B) {
	b.StopTimer()
	const filename = "go"
	src, err := ioutil.ReadFile(filename)
	if err != nil {
		panic(err)
	}
	fset := token.NewFileSet()
	file := fset.AddFile(filename, fset.Base(), len(src))
	b.SetBytes(int64(len(src)))
	var s Scanner
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		s.Init(file, src, nil, ScanComments)
		for {
			_, tok, _ := s.Scan()
			if tok == token.EOF {
				break
			}
		}
	}
}

func TestScanner_next(t *testing.T) {
	tests := []struct {
		name string
		s    *Scanner
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		tt.s.next()
	}
}

func TestScanner_Init(t *testing.T) {
	type args struct {
		file *token.File
		src  []byte
		err  errors.Handler
		mode Mode
	}
	tests := []struct {
		name string
		s    *Scanner
		args args
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		tt.s.Init(tt.args.file, tt.args.src, tt.args.err, tt.args.mode)
	}
}

func TestScanner_error(t *testing.T) {
	type args struct {
		offs int
		msg  string
	}
	tests := []struct {
		name string
		s    *Scanner
		args args
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		tt.s.error(tt.args.offs, tt.args.msg)
	}
}

func TestScanner_interpretLineComment(t *testing.T) {
	type args struct {
		text []byte
	}
	tests := []struct {
		name string
		s    *Scanner
		args args
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		tt.s.interpretLineComment(tt.args.text)
	}
}

func TestScanner_scanComment(t *testing.T) {
	tests := []struct {
		name string
		s    *Scanner
		want string
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		if got := tt.s.scanComment(); got != tt.want {
			t.Errorf("%q. Scanner.scanComment() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestScanner_findLineEnd(t *testing.T) {
	tests := []struct {
		name string
		s    *Scanner
		want bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		if got := tt.s.findLineEnd(); got != tt.want {
			t.Errorf("%q. Scanner.findLineEnd() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func Test_isLetter(t *testing.T) {
	type args struct {
		ch rune
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		if got := isLetter(tt.args.ch); got != tt.want {
			t.Errorf("%q. isLetter() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func Test_isDigit(t *testing.T) {
	type args struct {
		ch rune
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		if got := isDigit(tt.args.ch); got != tt.want {
			t.Errorf("%q. isDigit() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestScanner_scanIdentifier(t *testing.T) {
	tests := []struct {
		name string
		s    *Scanner
		want string
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		if got := tt.s.scanIdentifier(); got != tt.want {
			t.Errorf("%q. Scanner.scanIdentifier() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func Test_digitVal(t *testing.T) {
	type args struct {
		ch rune
	}
	tests := []struct {
		name string
		args args
		want int
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		if got := digitVal(tt.args.ch); got != tt.want {
			t.Errorf("%q. digitVal() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestScanner_scanMantissa(t *testing.T) {
	type args struct {
		base int
	}
	tests := []struct {
		name string
		s    *Scanner
		args args
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		tt.s.scanMantissa(tt.args.base)
	}
}

func TestScanner_scanNumber(t *testing.T) {
	type args struct {
		seenDecimalPoint bool
	}
	tests := []struct {
		name  string
		s     *Scanner
		args  args
		want  token.Token
		want1 string
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		got, got1 := tt.s.scanNumber(tt.args.seenDecimalPoint)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q. Scanner.scanNumber() got = %v, want %v", tt.name, got, tt.want)
		}
		if got1 != tt.want1 {
			t.Errorf("%q. Scanner.scanNumber() got1 = %v, want %v", tt.name, got1, tt.want1)
		}
	}
}

func TestScanner_scanEscape(t *testing.T) {
	type args struct {
		quote rune
	}
	tests := []struct {
		name string
		s    *Scanner
		args args
		want bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		if got, _ := tt.s.scanEscape(tt.args.quote); got != tt.want {
			t.Errorf("%q. Scanner.scanEscape() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestScanner_scanString(t *testing.T) {
	tests := []struct {
		name string
		s    *Scanner
		want string
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		if _, got := tt.s.scanString(rune(tt.name[0]), 1, 1); got != tt.want {
			t.Errorf("%q. Scanner.scanString() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func Test_stripCR(t *testing.T) {
	type args struct {
		b []byte
	}
	tests := []struct {
		name string
		args args
		want []byte
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		if got := stripCR(tt.args.b); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q. stripCR() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestScanner_scanRawString(t *testing.T) {
	tests := []struct {
		name string
		s    *Scanner
		want string
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		if got := tt.s.scanRawString(); got != tt.want {
			t.Errorf("%q. Scanner.scanRawString() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestScanner_skipWhitespace(t *testing.T) {
	tests := []struct {
		name string
		s    *Scanner
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		tt.s.skipWhitespace(1)
	}
}

func TestScanner_switch2(t *testing.T) {
	type args struct {
		tok0 token.Token
		tok1 token.Token
	}
	tests := []struct {
		name string
		s    *Scanner
		args args
		want token.Token
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		if got := tt.s.switch2(tt.args.tok0, tt.args.tok1); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q. Scanner.switch2() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestScanner_Scan(t *testing.T) {
	tests := []struct {
		name    string
		s       *Scanner
		wantPos token.Pos
		wantTok token.Token
		wantLit string
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		gotPos, gotTok, gotLit := tt.s.Scan()
		if !reflect.DeepEqual(gotPos, tt.wantPos) {
			t.Errorf("%q. Scanner.Scan() gotPos = %v, want %v", tt.name, gotPos, tt.wantPos)
		}
		if !reflect.DeepEqual(gotTok, tt.wantTok) {
			t.Errorf("%q. Scanner.Scan() gotTok = %v, want %v", tt.name, gotTok, tt.wantTok)
		}
		if gotLit != tt.wantLit {
			t.Errorf("%q. Scanner.Scan() gotLit = %v, want %v", tt.name, gotLit, tt.wantLit)
		}
	}
}
