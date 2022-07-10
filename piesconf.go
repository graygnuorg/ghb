// THE BEER-WARE LICENSE" (Revision 42):
// <gray@gnu.org> wrote this file.  As long as you retain this notice you
// can do whatever you want with this stuff. If we meet some day, and you
// think this stuff is worth it, you can buy me a beer in return.

package main

import (
	"fmt"
	"log"
	"os"
	"errors"
	"sort"
	"io"
	"io/ioutil"
	"unicode"
	"unicode/utf8"
	"strings"
	"net/url"
	"regexp"
	"strconv"
	"path/filepath"
)

// ----------------------------------
// Pies configuration file lexer
// ----------------------------------
const (
	TokenEOF = iota
	TokenWord
	TokenString
	TokenPunct
	TokenWS
	TokenComment

	bom = 0xFEFF
	eof = -1
)

type Locus struct {
	File string
	Line int
	Column int
}

func (l Locus) String() string {
	return fmt.Sprintf("%s:%d.%d", l.File, l.Line, l.Column)
}

type Token struct {
	Type int
	Text string
	Start Locus
}

func (t *Token) IsEOF() bool {
	return t.Type == TokenEOF
}

func (t *Token) IsText() bool {
	return t.Type == TokenWord || t.Type == TokenString
}

func (t *Token) IsWS() bool {
	return t.Type == TokenWS || t.Type == TokenComment
}

type Lexer struct {
	src []byte     // Source
	ch rune
	fileName string
	offset int     // Current position in src
	lineNo int     // Current line number
	lineOffset int  // Offset of the start of line in src
	tokens []*Token
}

func LexerNew(filename string) (*Lexer, error) {
	content, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	return &Lexer{src: content, fileName: filename}, nil
}

func (l *Lexer) Locus() Locus {
	return Locus{File: l.fileName, Line: l.lineNo + 1, Column: l.offset - l.lineOffset}
}

func (l *Lexer) Error(text string) error {
	return fmt.Errorf("%s: %s", l.Locus(), text)
}

func (l *Lexer) NextChar() (rune, error) {
	if l.offset < len(l.src) {
		if l.ch == '\n' {
			l.lineOffset = l.offset
			l.lineNo += 1
		}
		r, w := rune(l.src[l.offset]), 1
		switch {
		case r == 0:
			return eof, l.Error("illegal character NUL")
		case r >= utf8.RuneSelf:
			// not ASCII
			r, w = utf8.DecodeRune(l.src[l.offset:])
			if r == utf8.RuneError && w == 1 {
				return eof, l.Error("illegal UTF-8 encoding")
			} else if r == bom && l.offset > 0 {
				return eof, l.Error("illegal byte order mark")
			}
		}
		l.offset += w
		l.ch = r
	} else {
		l.ch = eof
	}
	return l.ch, nil
}

func (l *Lexer) CurChar() (rune, error) {
	if l.ch == 0 {
		return l.NextChar()
	} else {
		return l.ch, nil
	}
}

func (l *Lexer) scanWS() (token *Token, err error) {
	var sb strings.Builder
	locus := l.Locus()
	for l.ch != eof && unicode.IsSpace(l.ch) {
		sb.WriteRune(l.ch)
		_, err = l.NextChar()
		if err != nil {
			return
		}
	}
	token = &Token{Type: TokenWS, Text: sb.String(), Start: locus}
	return
}

func IsWord(r rune) bool {
	switch {
	case r == eof:
		return false
	case r == '_':
		return true
	case unicode.IsPunct(r):
		return false
	case unicode.IsSpace(r):
		return false
	}
	return true
}

func (l *Lexer) scanWord() (token *Token, err error) {
	var sb strings.Builder
	locus := l.Locus()
	for IsWord(l.ch) {
		sb.WriteRune(l.ch)
		_, err = l.NextChar()
		if err != nil {
			return
		}
	}
	token = &Token{Type: TokenWord, Text: sb.String(), Start: locus}
	return
}

func (l *Lexer) scanString() (token *Token, err error) {
	var sb strings.Builder
	locus := l.Locus()
	for {
		var r rune
		r, err = l.NextChar()
		if err != nil {
			return
		}
		if r == '"' {
			_, err = l.NextChar()
			if err != nil {
				return
			}
			break
		}
		if r == eof {
			break
		}
		if l.ch == '\\' {
			r, err = l.NextChar()
			if err != nil {
				return
			}
			switch r {
			case eof:
				goto end
			case 'a':
				sb.WriteRune('\a')
			case 'b':
				sb.WriteRune('\b')
			case 'f':
				sb.WriteRune('\f')
			case 'n':
				sb.WriteRune('\n')
			case 'r':
				sb.WriteRune('\r')
			case 't':
				sb.WriteRune('\t')
			case 'v':
				sb.WriteRune('\v')
			case '\\', '"':
				sb.WriteRune(r)
			case '\n':
				// skip
			default:
				sb.WriteRune('\\')
				sb.WriteRune(r)
			}
		} else {
			sb.WriteRune(l.ch)
		}
	}
end:
	token = &Token{Type: TokenString, Text: sb.String(), Start: locus}
	return
}

func (l *Lexer) scanInlineComment(r rune) (token *Token, err error) {
	var sb strings.Builder
	if r != 0 {
		sb.WriteRune(r)
	}
	locus := l.Locus()
	for l.ch != eof && l.ch != '\n' {
		sb.WriteRune(l.ch)
		_, err = l.NextChar()
		if err != nil {
			return
		}
	}
	token = &Token{Type: TokenComment, Text: sb.String(), Start: locus}
	return
}

func (l *Lexer) scanComment() (token *Token, err error) {
	var sb strings.Builder
	locus := l.Locus()
	sb.WriteRune('/')
	const (
		StateIn = iota
		StateStar
	)
	state := StateIn
	for l.ch != eof {
		sb.WriteRune(l.ch)
		var r rune
		r, err = l.NextChar()
		if err != nil {
			return
		}
		if state == StateIn {
			if r == '*' {
				state = StateStar
			} else {
				state = StateIn
			}
		} else if state == StateStar {
			if r == '/' {
				sb.WriteRune(r)
				_, err = l.NextChar()
				break
			}
		} else {
			state = StateIn
		}
	}
	token = &Token{Type: TokenComment, Text: sb.String(), Start: locus}
	return
}

func (l *Lexer) NextToken() (token *Token, err error) {
	var r rune

	r, err = l.CurChar()
	if err != nil {
		return
	}

	switch {
	case r == eof:
		token = &Token{Type: TokenEOF, Start: l.Locus()}

	case r == '#':
		token, err = l.scanInlineComment(0)

	case r == '/':
		var r1 rune
		locus := l.Locus()
		r1, err = l.NextChar()
		if err != nil {
			return
		}
		if r1 == '*' {
			token, err = l.scanComment()
		} else if r1 == '/' {
			token, err = l.scanInlineComment('/')
		} else {
			token = &Token{Type: TokenPunct, Text: string(r), Start: locus}
		}

	case r == '"':
		token, err = l.scanString()

	case unicode.IsPunct(r):
		token = &Token{Type: TokenPunct, Text: string(r), Start: l.Locus()}
		_, err = l.NextChar()

	case unicode.IsSpace(r):
		token, err = l.scanWS()

	default:
		token, err = l.scanWord()
	}
	//fmt.Printf("T %v\n", token)
	l.tokens = append(l.tokens, token)
	return
}

func (l *Lexer) NextNWSToken() (token *Token, err error) {
	for {
		token, err = l.NextToken()
		if err != nil || token.IsEOF() || !token.IsWS() {
			break
		}
	}
	return
}

func (l *Lexer) SkipStatement() error {
	nestingLevel := 0
	for {
		t, err := l.NextNWSToken()
		if err != nil {
			return err
		}
		if t.IsEOF() {
			break
		}
		if t.Type == TokenPunct {
			if t.Text == "{" {
				nestingLevel += 1
			} else if t.Text == "}" {
				nestingLevel -= 1
				if nestingLevel == 0 {
					break
				}
			} else if nestingLevel == 0 && t.Text == ";" {
				break
			}
		}
	}
	return nil
}

func (l *Lexer) SkipBlock() error {
	nestingLevel := 1
	for {
		t, err := l.NextNWSToken()
		if err != nil {
			return err
		}
		if t.IsEOF() {
			break
		}

		if t.Type == TokenPunct {
			if t.Text == "{" {
				nestingLevel += 1
			} else if t.Text == "}" {
				nestingLevel -= 1
				if nestingLevel == 0 {
					//fmt.Println(l.Error("End of block"))
					break
				}
			}
		}
	}
	return nil
}

// ----------------------------------
// Pies configuration file parser
// ----------------------------------

type Runner struct {
	Num int
	TokenStart int
	TokenEnd int
	Dir string
}

type PiesConfig struct {
	FileName string
	ControlURL *url.URL
	Runners map[string][]Runner
	Tokens []*Token
}

func (pc *PiesConfig) ParseControl(l *Lexer) error {
	t, err := l.NextNWSToken()
	if err != nil {
		return err
	}
	if t.IsEOF() {
		return nil
	}

	if !(t.Type == TokenPunct && t.Text == "{") {
		return l.SkipStatement()
	}

	t, err = l.NextNWSToken()
	if err != nil {
		return err
	}
	if t.Type == TokenWord && t.Text == "socket" {
		t, err := l.NextNWSToken()
		if err != nil {
			return err
		}
		if t.IsText() {
			pc.ControlURL, err = url.Parse(t.Text)
			if err != nil {
				// FIXME: locus (column)
				log.Printf("%s: can't parse URL: %v", t.Start, err)
			}
		}
	}
	return l.SkipBlock()
}

func (pc *PiesConfig) Write(w io.Writer) (err error) {
	for _, t := range pc.Tokens {
		text := t.Text
		if t.Type == TokenString {
			text = `"` + text + `"` // FIXME: escapes
		}
		_, err = io.WriteString(w, text)
		if err != nil {
			break
		}
	}
	return
}

var runnerNameRx = regexp.MustCompile(`^(.+?)/(\d+)`)

func (pc *PiesConfig) ParseComponent(l *Lexer) error {
	start := len(l.tokens) - 1

	t, err := l.NextNWSToken()
	if err != nil {
		return err
	}
	if t.IsEOF() {
		return nil
	}

	runnerName := ""
	r := Runner{TokenStart: start}
	if t.IsText() {
		if m := runnerNameRx.FindStringSubmatch(t.Text); m != nil {
			n, err := strconv.Atoi(m[2])
			if err == nil {
				runnerName = m[1]
				r.Num = n
			}
		}
	}
	if err := l.SkipStatement(); err != nil {
		return err
	}
	if runnerName != "" {
		r.TokenEnd = len(l.tokens) - 1
		var i int
		for i = r.TokenStart; i < r.TokenEnd; i += 1 {
			if l.tokens[i].Type == TokenWord && l.tokens[i].Text == `chdir` {
				i += 1
				break
			}
		}
		for i < r.TokenEnd && l.tokens[i].IsWS() {
			i += 1
		}
		if i < r.TokenEnd && l.tokens[i].IsText() {
			r.Dir = l.tokens[i].Text
			pc.Runners[runnerName] = append(pc.Runners[runnerName], r)
		}
	}
	return nil
}

func (pc *PiesConfig) Save() error {
	tempfile, err := ioutil.TempFile(filepath.Dir(pc.FileName), filepath.Base(pc.FileName) + `.*`)
	if err != nil {
		return fmt.Errorf("can't create temporary file: %v", err)
	}
	tempname := tempfile.Name()
	err = pc.Write(tempfile)
	if err != nil {
		return err
	}
	tempfile.Close()

	err = os.Remove(pc.FileName)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("can't remove %s: %v", pc.FileName, err)
	}

	err = os.Rename(tempname, pc.FileName)
	if err != nil {
		return fmt.Errorf("can't rename %s to %s: %v", tempname, pc.FileName, err)
	}

	return nil
}

func ParsePiesConfig(filename string) (*PiesConfig, error) {
	pc := &PiesConfig{FileName: filename, Runners: make(map[string][]Runner)}

	l, err := LexerNew(filename)
	if err != nil {
		return nil, err
	}

	for {
		t, err := l.NextNWSToken()
		if err != nil {
			return nil, err
		}
		if t.IsEOF() {
			break
		}

		if t.Type == TokenWord {
			switch t.Text {
			case "control":
				pc.ParseControl(l)

			case "component":
				pc.ParseComponent(l)
			}
		}
	}
	for p, _ := range pc.Runners {
		sort.Slice(pc.Runners[p], func (i, j int) bool { return pc.Runners[p][i].Num < pc.Runners[p][j].Num })
	}
	pc.Tokens = l.tokens
	return pc, nil
}

func (pc *PiesConfig) AddRunner(name string) error {
	text, err := ExpandTemplate(config.ComponentTemplate, name)
	if err != nil {
		return err
	}
	pc.Tokens = append(pc.Tokens, &Token{Type: TokenWord, Text: text})
	// l := &Lexer{src: []byte(text), fileName: "-"}
	// for {
	//	t, err := l.NextToken()
	//	if err != nil {
	//		return err
	//	}
	//	if t.IsEOF() {
	//		break
	//	}
	// }
	// pc.Tokens = append(pc.Tokens, l.tokens...)
	return nil
}
