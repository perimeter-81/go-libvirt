// Copyright 2017 The go-libvirt Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lvgen

// The libvirt API is divided into several categories. (Gallia est omnis divisa
// in partes tres.) The generator will output code for each category in a
// package underneath the go-libvirt directory.

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/template"
	"unicode"
	"unicode/utf8"
)

var keywords = map[string]int{
	"hyper":    HYPER,
	"int":      INT,
	"short":    SHORT,
	"char":     CHAR,
	"bool":     BOOL,
	"case":     CASE,
	"const":    CONST,
	"default":  DEFAULT,
	"double":   DOUBLE,
	"enum":     ENUM,
	"float":    FLOAT,
	"opaque":   OPAQUE,
	"string":   STRING,
	"struct":   STRUCT,
	"switch":   SWITCH,
	"typedef":  TYPEDEF,
	"union":    UNION,
	"unsigned": UNSIGNED,
	"void":     VOID,
	"program":  PROGRAM,
	"version":  VERSION,
}

// ConstItem stores an const's symbol and value from the parser. This struct is
// also used for enums.
type ConstItem struct {
	Name string
	Val  string
}

// Generator holds all the information parsed out of the protocol file.
type Generator struct {
	// Enums holds the list of enums found by the parser.
	Enums []ConstItem
	// Consts holds all the const items found by the parser.
	Consts []ConstItem
}

// Gen accumulates items as the parser runs, and is then used to produce the
// output.
var Gen Generator

// CurrentEnumVal is the auto-incrementing value assigned to enums that aren't
// explicitly given a value.
var CurrentEnumVal int64

// oneRuneTokens lists the runes the lexer will consider to be tokens when it
// finds them. These are returned to the parser using the integer value of their
// runes.
var oneRuneTokens = `{}[]<>(),=;:*`

// Generate will output go bindings for libvirt. The lvPath parameter should be
// the path to the root of the libvirt source directory to use for the
// generation.
func Generate(proto io.Reader) error {
	lexer, err := NewLexer(proto)
	if err != nil {
		return err
	}
	go lexer.Run()
	parser := yyNewParser()
	yyErrorVerbose = true
	// Turn this on if you're debugging.
	// yyDebug = 3
	rv := parser.Parse(lexer)
	if rv != 0 {
		return fmt.Errorf("failed to parse libvirt protocol: %v", rv)
	}

	// Generate and write the output.
	wr, err := os.Create("../constants/constants.gen.go")
	if err != nil {
		return err
	}
	defer wr.Close()

	err = genGo(wr)

	return err
}

func genGo(wr io.Writer) error {
	// Enums and consts from the protocol definition both become go consts in
	// the generated code. We'll remove "REMOTE_" and then camel-case the
	// name before making each one a go constant.
	for ix, en := range Gen.Enums {
		Gen.Enums[ix].Name = constNameTransform(en.Name)
	}
	for ix, en := range Gen.Consts {
		Gen.Consts[ix].Name = constNameTransform(en.Name)
	}

	t, err := template.ParseFiles("constants.tmpl")
	if err != nil {
		return err
	}
	if err := t.Execute(wr, Gen); err != nil {
		return err
	}
	return nil
}

// constNameTransform changes an upcased, snake-style name like
// REMOTE_PROTOCOL_VERSION to a comfortable Go name like ProtocolVersion. It
// also tries to upcase abbreviations so a name like DOMAIN_GET_XML becomes
// DomainGetXML, not DomainGetXml.
func constNameTransform(name string) string {
	nn := fromSnakeToCamel(strings.TrimPrefix(name, "REMOTE_"))
	nn = fixAbbrevs(nn)
	return nn
}

// fromSnakeToCamel transmutes a snake-cased string to a camel-cased one. All
// runes that follow an underscore are up-cased, and the underscores themselves
// are omitted.
//
// ex: "PROC_DOMAIN_GET_METADATA" -> "ProcDomainGetMetadata"
func fromSnakeToCamel(s string) string {
	buf := make([]rune, 0, len(s))
	// Start with an upper-cased rune
	hump := true

	for _, r := range s {
		if r == '_' {
			hump = true
		} else {
			var transform func(rune) rune
			if hump == true {
				transform = unicode.ToUpper
			} else {
				transform = unicode.ToLower
			}
			buf = append(buf, transform(r))
			hump = false
		}
	}

	return string(buf)
}

// abbrevs is a list of abbreviations which should be all upper-case in a name.
// (This is really just to keep the go linters happy and to produce names that
// are intuitive to a go developer.)
var abbrevs = []string{"Xml", "Io", "Uuid", "Cpu", "Id", "Ip"}

// fixAbbrevs up-cases all instances of anything in the 'abbrevs' array. This
// would be a simple matter, but we don't want to upcase an abbreviation if it's
// actually part of a larger word, so it's not so simple.
func fixAbbrevs(s string) string {
	for _, a := range abbrevs {
		for loc := 0; ; {
			loc = strings.Index(s[loc:], a)
			if loc == -1 {
				break
			}
			r := 'A'
			if len(a) < len(s[loc:]) {
				r, _ = utf8.DecodeRune([]byte(s[loc+len(a):]))
			}
			if unicode.IsLower(r) == false {
				s = s[:loc] + strings.Replace(s[loc:], a, strings.ToUpper(a), 1)
			}
			loc++
		}
	}
	return s
}

//---------------------------------------------------------------------------
// Routines called by the parser's actions.
//---------------------------------------------------------------------------

// StartEnum is called when the parser has found a valid enum.
func StartEnum() {
	// Set the automatic value var to -1; it will be incremented before being
	// assigned to an enum value.
	CurrentEnumVal = -1
}

// AddEnum will add a new enum value to the list.
func AddEnum(name, val string) error {
	ev, err := parseNumber(val)
	if err != nil {
		return fmt.Errorf("invalid enum value %v = %v", name, val)
	}
	return addEnum(name, ev)
}

// AddEnumAutoVal adds an enum to the list, using the automatically-incremented
// value. This is called when the parser finds an enum definition without an
// explicit value.
func AddEnumAutoVal(name string) error {
	CurrentEnumVal++
	return addEnum(name, CurrentEnumVal)
}

func addEnum(name string, val int64) error {
	Gen.Enums = append(Gen.Enums, ConstItem{name, fmt.Sprintf("%d", val)})
	CurrentEnumVal = val
	return nil
}

// AddConst adds a new constant to the parser's list.
func AddConst(name, val string) error {
	_, err := parseNumber(val)
	if err != nil {
		return fmt.Errorf("invalid const value %v = %v", name, val)
	}
	Gen.Consts = append(Gen.Consts, ConstItem{name, val})
	return nil
}

// parseNumber makes sure that a parsed numerical value can be parsed to a 64-
// bit integer.
func parseNumber(val string) (int64, error) {
	base := 10
	if strings.HasPrefix(val, "0x") {
		base = 16
		val = val[2:]
	}
	n, err := strconv.ParseInt(val, base, 64)
	return n, err
}
