// THE BEER-WARE LICENSE" (Revision 42):
// <gray@gnu.org> wrote this file.  As long as you retain this notice you
// can do whatever you want with this stuff. If we meet some day, and you
// think this stuff is worth it, you can buy me a beer in return.

package main

import (
	"os"
	"os/exec"
	"os/user"
	"net/http"
	"io"
	"fmt"
	"path/filepath"
	"log"
	"strings"
	"unicode"
	"unicode/utf8"
	"encoding/json"
	"io/ioutil"
	"regexp"
	"strconv"
	"syscall"
	"errors"
	"net"
	"net/url"
	"time"
	"text/template"
	"context"
	"sort"
	"reflect"
	"golang.org/x/mod/semver"
	"github.com/pborman/getopt/v2"
	"gopkg.in/yaml.v2"
	"github.com/graygnuorg/go-gdbm"
)

type Config struct {
	Url string                `yaml:"url" rem:"URL of the runner application tarball"`
	RootDir string            `yaml:"root_dir" rem:"Root directory" verify:"dir_exist"`
	RunnersDir string         `yaml:"runners_dir" rem:"Directory for storing runners" verify:"dir_exist"  rel:"RootDir"`
	CacheDir string           `yaml:"cache_dir" rem:"Cache directory" verify:"dir_exist" rel:"RootDir"`
	Tar string                `yaml:"tar" rem:"Tar binary" verify:"exe"`
	Pies string               `yaml:"pies" rem:"Pies binary" verify:"pies_version"`
	PiesConfigFile string     `yaml:"pies_config_file" rem:"Pies configuration file name" verify:"pies_config" rel:"RootDir"`
	ComponentTemplate string  `yaml:"component_template" rem:"Template for runner components" verify:"component_template"`
}

var config = Config{
	Url: `https://github.com/actions/runner/releases/download/v2.294.0/actions-runner-linux-x64-2.294.0.tar.gz`,
	RunnersDir: ``,
	CacheDir: ``,
	Tar: `tar`,
	Pies: `pies`,
	PiesConfigFile: ``,
	// FIXME: Make sure the lines in the literal below are indented using spaces, not tabs.
	// This way yaml marshaller prints the value in readable form.
        ComponentTemplate: `component {{ RunnerName }} {
        mode respawn;
        chdir "{{ Config.RunnersDir }}/{{ RunnerName }}";
        stderr syslog daemon.err;
        stdout syslog daemon.info;
        flags siggroup;
        command "./run.sh";
}
`,
}

var PiesConfigStub = `
pidfile {{ Config.RootDir }}/pies.pid;
control {
	socket "inet://127.0.0.1:8073";
}
`

func GetHomeDir() (dir string) {
	dir = os.Getenv("HOME")
	if dir == "" {
		if user, err := user.Current(); err == nil {
			dir = user.HomeDir
		} else {
			dir = "/"
		}
	}
	return
}

func CheckDir(dirname string) error {
	st, err := os.Stat(dirname)
	switch {
	case os.IsNotExist(err):
		fmt.Printf("Creating directory %s\n", dirname)
		err := os.MkdirAll(dirname, 0750)
		if err != nil {
			return fmt.Errorf("Can't create directory %s: %v", dirname, err)
		}
	case err == nil:
		if !st.IsDir() {
			return fmt.Errorf("%s exists, but is not a directory", dirname)
		}

	default:
		return fmt.Errorf("can't stat %s: %v", dirname, err)
	}
	return nil
}

func ReadConfig() (ok bool, filename string) {
	env_name := os.Getenv("GHB_CONFIG")
	if env_name == "" {
		filename = filepath.Join(GetHomeDir(), `ghb.conf`)
	} else {
		filename = env_name
	}
	content, err := ioutil.ReadFile(filename)
	if err == nil {
		err = yaml.Unmarshal([]byte(content), &config)
		if err != nil {
			log.Fatalf("%s: %v", filename, err)
		}
		ok = true
	} else if env_name == "" && errors.Is(err, os.ErrNotExist) {
		// OK, default config is not required to exist
		ok = false
	} else {
		log.Panic(err)
	}

	// Provide missing defaults; resolve relative file names
	if config.RootDir == "" {
		config.RootDir = "GHB"
	}
	if !filepath.IsAbs(config.RootDir) {
		config.RootDir = filepath.Join(GetHomeDir(), config.RootDir)
	}

	if config.RunnersDir == "" {
		config.RunnersDir = `runners`
	}
	if !filepath.IsAbs(config.RunnersDir) {
		config.RunnersDir = filepath.Join(config.RootDir, config.RunnersDir)
	}

	if config.CacheDir == "" {
		config.CacheDir = `cache`
	}
	if !filepath.IsAbs(config.CacheDir) {
		config.CacheDir = filepath.Join(config.RootDir, config.CacheDir)
	}

	if config.PiesConfigFile == "" {
		config.PiesConfigFile = `pies.conf`
	}
	if !filepath.IsAbs(config.PiesConfigFile) {
		config.PiesConfigFile = filepath.Join(config.RootDir, config.PiesConfigFile)
	}
	return
}

func VerifyStruct(obj interface{}, verbose bool) (result bool) {
	result = true

	verifier := map[string]func(reflect.Value) error {
		"dir_exist": func(v reflect.Value) error {
			dirname, _ := v.Interface().(string)
			st, err := os.Stat(dirname)
			if err == nil {
				if !st.IsDir() {
					err = fmt.Errorf("%s exists, but is not a directory", dirname)
				}
			}
			return err
		},
		"exe": func(v reflect.Value) error {
			exe, _ := v.Interface().(string)
			cmd := exec.Command(exe, "--version")
			cmd.Stdout = nil
			cmd.Stderr = nil
			return cmd.Run()
		},
		"pies_version": func(v reflect.Value) error {
			exe, _ := v.Interface().(string)
			return CheckPiesCommand(exe)
		},
		"pies_config": func(v reflect.Value) error {
			filename, _ := v.Interface().(string)
			if _, err := os.Stat(filename); err != nil {
				return err
			}
			cmd := exec.Command(config.Pies, "--config-file", filename, "--lint")
			cmd.Stdout = nil
			cmd.Stderr = nil
			if err := cmd.Run(); err != nil {
				if werr, ok := err.(*exec.ExitError); ok {
					if s := werr.Error(); s == "78" {
						return errors.New("syntax check failed")
					}
				}
				return err
			}
			return nil
		},
		"component_template": func(v reflect.Value) error {
			text, _ := v.Interface().(string)
			_, err := ExpandTemplate(text, "runner_0");
			return err
		},
	}

	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		log.Printf("Passed object has unsupported type: %s", v.Kind())
		return false
	}
	t := v.Type()

	if verbose {
		fmt.Println("Verifying configuration")
	}

	for i := 0; i < v.NumField(); i++ {
		f := t.Field(i)
		name := f.Tag.Get(`yaml`)
		if name == "" {
			continue
		}
		vt := f.Tag.Get(`verify`)
		if vt == "" {
			continue
		}

		if ckf, ok := verifier[vt]; ok {
			if verbose {
				fmt.Printf("  %s = %#v: ",name, v.Field(i))
			}
			if err := ckf(v.Field(i)); err != nil {
				if verbose {
					fmt.Println(err)
				}

				result = false
			} else if verbose {
				fmt.Println("OK")
			}
		}
	}
	return
}

func FinalizeConfig() {
	if err := CheckDir(config.RootDir); err != nil {
		log.Panic(err)
	}
	if err := CheckDir(config.RunnersDir); err != nil {
		log.Panic(err)
	}
	if err := CheckDir(config.CacheDir); err != nil {
		log.Panic(err)
	}

	if _, err := os.Stat(config.PiesConfigFile); err == nil {
		// File exists, Ok
	} else if os.IsNotExist(err) {
		if err := CreateFileFromStub(config.PiesConfigFile, PiesConfigStub); err != nil {
			log.Panic(err)
		}
	} else {
		log.Fatalf("can't stat %s: %v", config.PiesConfigFile, err)
	}
}

func CreateFileFromStub(filename, stub string) error {
	fmt.Printf("Creating file %s\n", filename)
	tmpl, err := template.New("file").Funcs(template.FuncMap{
		"Config": func () *Config { return &config },
	}).Parse(stub)
	if err != nil {
		return fmt.Errorf("can't parse template: %v", err)
	}

	if file, err := os.Create(filename); err == nil {
		err = tmpl.Execute(file, nil)
		file.Close()
		if err != nil {
			return fmt.Errorf("can't write file %s: %v", filename, err)
		}
	} else {
		return fmt.Errorf("can't create file %s: %v", filename, err)
	}
	return nil
}

func download(name string) error {
	out, err := os.Create(name)
	if err != nil  {
		return err
	}
	defer out.Close()

	resp, err := http.Get(config.Url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	_, err = io.Copy(out, resp.Body)
	if err != nil  {
		os.Remove(name)
	}
	return err
}

func GetArchiveFile() (filename string, err error) {
	filename = filepath.Join(config.CacheDir, filepath.Base(config.Url))
	_, err = os.Stat(filename)
	switch {
	case err == nil:
		fmt.Printf("Using cached copy %s\n", filename)
		break
	case os.IsNotExist(err):
		fmt.Printf("Downloading %s\n", config.Url)
		err = download(filename)
	default:
		err = fmt.Errorf("Can't stat %s: %v", filename, err)
	}

	return
}

func InstallToDir(projectName, projectUrl, projectToken, labels string) error {
	arc, err := GetArchiveFile()
	if err != nil {
		return err
	}

	dirname := filepath.Join(config.RunnersDir, projectName)
	err = os.MkdirAll(dirname, 0750)
	if err != nil {
		return fmt.Errorf("Can't create %s: %v", dirname, err)
	}

	fmt.Printf("Extracting to %s\n", dirname)
	cmd := exec.Command(config.Tar, "-C", dirname, "-x", "-f", arc)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Error running %s: %v", config.Tar, err)
	}

	var cwd string
	cwd, err = os.Getwd()
	if err != nil {
		return fmt.Errorf("Can't get cwd: %v", err)
	}
	defer os.Chdir(cwd)

	err = os.Chdir(dirname)
	if err != nil {
		return fmt.Errorf("Can't chdir to %s: %v", dirname, err)
	}

	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("Can't determine hostname: %v", err)
	}

	name := hostname + `_` + projectName

	fmt.Printf("Configuring %s\n", name)
	cmdline := []string{
		"--name", name,
		"--url", projectUrl,
		"--token", projectToken,
		"--unattended",
	}
	if labels != "" {
		cmdline = append(cmdline, "--labels", labels)
	}
	cmd = exec.Command("./config.sh", cmdline...)

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Error running config.sh: %v", err)
	}

	return nil
}

func ExpandTemplate(text, runnerName string) (string, error) {
	tmpl, err := template.New("component").Funcs(template.FuncMap{
		"RunnerName": func () string { return runnerName },
		"Config": func () *Config { return &config },
	}).Parse(text)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	err = tmpl.Execute(&sb, nil)
	if err != nil {
		return "", err
	}
	return sb.String(), nil
}

// ----------------------------------
// Token database
// ----------------------------------

var (
	ErrTokenNotFound = errors.New("Token not found")
)

func SaveToken(key string, token GHToken) error {
	js, err := json.Marshal(token)
	if err != nil {
		return nil
	}

	dbname := filepath.Join(config.CacheDir, `token.db`)
	db, err := gdbm.Open(dbname, gdbm.ModeWrcreat)
	if err != nil {
		return fmt.Errorf("can't open database file %s for update: %v", dbname, err)
	}
	defer db.Close()
	if err := db.Store([]byte(key), js, true); err != nil {
		return fmt.Errorf("can't store key %s: %v", key, err)
	}
	return nil
}

func FetchRawToken(key string) (GHToken, error) {
	dbname := filepath.Join(config.CacheDir, `token.db`)
	db, err := gdbm.Open(dbname, gdbm.ModeReader)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = ErrTokenNotFound
		}
		return GHToken{}, err
	}
	defer db.Close()
	if js, err := db.Fetch([]byte(key)); err == nil {
		var tok GHToken
		err := json.Unmarshal(js, &tok)
		return tok, err
	} else if errors.Is(err, gdbm.ErrItemNotFound) {
		return GHToken{}, ErrTokenNotFound
	} else {
		return GHToken{}, err
	}
}

func FetchToken(key string) (string, error) {
	if tok, err := FetchRawToken(key); err == nil {
		if time.Now().Before(tok.ExpiresAt) {
			return tok.Token, nil
		}
		return "", ErrTokenNotFound
	} else {
		return "", err
	}
}

func DeleteToken(key string) error {
	dbname := filepath.Join(config.CacheDir, `token.db`)
	db, err := gdbm.Open(dbname, gdbm.ModeWrcreat)
	if err != nil {
		return fmt.Errorf("can't open database file %s for update: %v", dbname, err)
	}
	defer db.Close()
	return db.Delete([]byte(key))
}

func PrefixIterator(pfx string) (func () (string, GHToken, error), error) {
	dbname := filepath.Join(config.CacheDir, `token.db`)
	db, err := gdbm.Open(dbname, gdbm.ModeReader)
	if err != nil {
		return nil, err
	}

	next := db.Iterator()
	return func() (key string, tok GHToken, err error) {
		for {
			var b []byte
			b, err = next()
			if err == nil {
				key = string(b)
				if key != pfx && strings.TrimPrefix(key, pfx) != key {
					var js []byte
					if js, err = db.Fetch(b); err == nil {
						if err = json.Unmarshal(js, &tok); err == nil {
							return
						}
					}
					break
				}
			} else {
				break
			}
		}
		db.Close()
		return
	}, nil
}


func getGitHubToken(key, pat string) (token GHToken, err error) {
	var req *http.Request
	req, err = http.NewRequest(http.MethodPost, `https://api.github.com` + key, nil)
	if err != nil {
		return
	}

	req.Header.Add("Accept", "application/vnd.github+json")
	req.Header.Add("Authorization", "token " + pat)
	fmt.Printf("Getting token for %s\n", req.URL.String())
	//fmt.Printf("%#v",req)
	var resp *http.Response
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var body []byte
	if body, err = ioutil.ReadAll(resp.Body); err != nil {
		return
	}
	if resp.StatusCode == 201 {
		err = json.Unmarshal(body, &token)
	} else {
		err = ErrTokenNotFound
	}
	return
}

func GetPATKey(key string) (patkey string, ispat bool) {
	for _, pfx := range GHEntityPrefix {
		if s := strings.TrimPrefix(key, pfx); s != key {
			if n := strings.IndexRune(s, '/'); n == -1 {
				patkey = key
				ispat = true
			} else {
				patkey = pfx
				ispat = false
				patkey = patkey + s[:n]
			}
			break
		}
	}
	return
}

func GetToken(key string) (string, error) {
	if token, err := FetchToken(key); err == nil {
		return token, err
	} else if errors.Is(err, ErrTokenNotFound) {
		if patkey, ispat := GetPATKey(key); ispat {
			return "", err
		} else if token, err := FetchToken(patkey); err == nil {
			if tok, err := getGitHubToken(key, token); err != nil {
				return "", err
			} else {
				SaveToken(key, tok)
				return tok.Token, err
			}
		} else {
			return "", err
		}
	} else {
		return "", err
	}
}

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

var runnerNameRx = regexp.MustCompile(`^(.+?)_(\d+)`)

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

// ----------------------------------
// Pies CTL API
// ----------------------------------

type PiesResponse struct {
	Status string
	Message string
	//Parser_messages []string
}

var allIPRx = regexp.MustCompile(`^(0\.0\.0\.0)?(:.+)`)

func PiesClient(controlURL *url.URL, method, path string, retval interface{}) (reterr error) {
	clt := http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				switch controlURL.Scheme {
				case `local`, `file`, `unix`:
					return net.Dial(`unix`, controlURL.Path)
				case `inet`:
					host := controlURL.Host
					if m := allIPRx.FindStringSubmatch(host); m != nil {
						host = `127.0.0.1` + m[2]
					}
					return net.Dial(`tcp`, host)
				}
				return nil, errors.New("Scheme not implemented")
			},
		},
	}

	rurl := &url.URL{Scheme: "http", Path: path}
	if controlURL.Scheme == "inet" {
		rurl.Host = controlURL.Host
	} else {
		rurl.Host = "localhost"
	}

	req, err := http.NewRequest(method, rurl.String(), nil)
	if err != nil {
		reterr  = err
		return
	}
	resp, err := clt.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			reterr = fmt.Errorf("can't connect to pies: not running?")
		} else {
			reterr = fmt.Errorf("can't query: %v", err)
		}
		return
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		reterr = fmt.Errorf("can't read response: %v", err)
		return
	}

	if retval != nil {
		reterr = json.Unmarshal(body, retval)
	}
	return
}

func PiesStopInstance(controlURL *url.URL) error {
	var resp PiesResponse
	if err := PiesClient(controlURL, http.MethodDelete, `/instance/PID`, &resp); err != nil {
		return err
	}
	if resp.Status != "OK" {
		return errors.New(resp.Message)
	}

	return nil
}

func PiesRestartInstance(controlURL *url.URL) error {
	var resp PiesResponse
	if err := PiesClient(controlURL, http.MethodPut, `/instance/PID`, &resp); err != nil {
		return err
	}
	if resp.Status != "OK" {
		return errors.New(resp.Message)
	}

	return nil
}

func PiesReloadConfig(controlURL *url.URL) error {
	var rresp PiesResponse
	err := PiesClient(controlURL, http.MethodPut, `/conf/runtime`, &rresp)
	if err != nil {
		return err
	}

	if rresp.Status != "OK" {
		return errors.New(rresp.Message)
	}
	return nil
}

type PiesInstanceInfo struct {
	PID int             `json:"PID"`
	Args []string       `json:"argv"`
	Binary string       `json:"binary"`
	InstanceName string `json:"instance"`
	PackageName string  `json:"package"`
	Version string      `json:"version"`
}

func GetPiesInstanceInfo(controlURL *url.URL) (err error, info PiesInstanceInfo) {
	err = PiesClient(controlURL, http.MethodGet, `/instance`, &info)
	return
}

type PiesComponentInfo struct {
	Mode string         `json:"mode"`
	Status string       `json:"status"`
	PID int             `json:"PID"`
	URL string          `json:"URL"`
	Service string      `json:"service"`
	TcpMUXMaster string `json:"master"`
	Runlevels string    `json:"runlevels"`
	WakeupTime int      `json:"wakeup-time"`
	Args []string       `json:"argv"`
	Command string      `json:"command"`
}

func GetPiesComponentInfo(controlURL *url.URL) (err error, info []PiesComponentInfo) {
	err = PiesClient(controlURL, http.MethodGet, `/programs`, &info)
	return
}

type Action struct {
	Action func ([]string)
	Help string
}

type Optset struct {
	*getopt.Set
	InitArgs []string
	Command string
	hflag bool
}

func NewOptset(args []string) (optset *Optset) {
	optset = new(Optset)
	optset.Set = getopt.New()
	optset.InitArgs = args
	optset.Command = filepath.Base(os.Args[0]) + " " + args[0]
	optset.SetProgram(optset.Command)
	optset.FlagLong(&optset.hflag, "help", 'h', "Show this help")
	return
}

func (optset *Optset) Parse() {
	if err := optset.Getopt(optset.InitArgs, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		optset.PrintUsage(os.Stderr)
		os.Exit(1)
	}

	if optset.hflag {
		optset.PrintUsage(os.Stdout)
		os.Exit(0)
	}
}

type EntityOptset struct {
	*Optset
	Entity entityValue
}

type entityValue struct {
	Type int
	Name string
}

func (ent *entityValue) Set(value string, opt getopt.Option) error {
        switch opt.LongName() {
	case `org`:
		ent.Type = EntityOrg
	case `enterprise`:
		ent.Type = EntityEnterprise
	case `repo`:
		ent.Type = EntityRepo
	}
	ent.Name = value

        return nil
}

func (ent *entityValue) String() string {
        return ent.Name
}

func NewEntityOptset(args []string) (optset *EntityOptset) {
	optset = new(EntityOptset)
	optset.Optset = NewOptset(args)
	optset.FlagLong(&optset.Entity, "org", 0, "Organization").SetGroup("entity")
	optset.FlagLong(&optset.Entity, "enterprise", 0, "Enterprize").SetGroup("entity")
	optset.FlagLong(&optset.Entity, "repo", 0, "Repository").SetGroup("entity")
	return optset
}

func (optset *EntityOptset) Parse() {
	optset.Optset.Parse()
	if optset.Entity.Name == "" {
		log.Fatalf("One of --org, --enterprise, or --repo must be given")
	}
}

var actions map[string]Action

func HelpAction(args []string) {
	commands := make([]string, len(actions))
	i := 0
	for com := range actions {
		commands[i] = com
		i++
	}
	sort.Strings(commands)
	fmt.Printf("usage: %s COMMAND [ARGS...]\n", filepath.Base(os.Args[0]))
	fmt.Printf("Available commands:\n")
	for _, com := range commands {
		fmt.Printf("    %-10s  %s\n", com, actions[com].Help)
	}
	fmt.Printf("To obtain a help on a particular command, run: %s COMMAND -h\n", filepath.Base(os.Args[0]))
}

func ListAction(args []string) {
	ReadConfig()

	optset := NewOptset(args)
	optset.SetParameters("")
	verbose := false
	optset.FlagLong(&verbose, "verbose", 'v', "Verbosely list each runner location")
	optset.Parse()

	args = optset.Args()
	if len(args) > 0 {
		log.Fatalf("too many arguments; try `%s --help' for assistance", optset.Command)
	}

	pc, err := ParsePiesConfig(config.PiesConfigFile)
	if err != nil {
		log.Panic(err)
	}

	var projects []string
	for p, _ := range pc.Runners {
		projects = append(projects, p)
	}
	sort.Strings(projects)
	for _, p := range projects {
		fmt.Printf("%-32.32s %4d %d\n", p, len(pc.Runners[p]), pc.Runners[p][len(pc.Runners[p])-1].Num + 1)
		if verbose {
			for _, r := range pc.Runners[p] {
				fmt.Printf(" %d: %s %s - %s\n", r.Num, r.Dir, pc.Tokens[r.TokenStart].Start, pc.Tokens[r.TokenEnd].Start)
			}
		}
	}
}

func AddAction(args []string) {
	ReadConfig()
	FinalizeConfig()
	optset := NewEntityOptset(args)
	optset.SetParameters("[PROJECTNAME]")

	var (
		ProjectName string
		ProjectUrl string
		ProjectToken string
		labels string
	)
	optset.FlagLong(&ProjectUrl, "url", 'u', "Project URL", "URL")
	optset.FlagLong(&ProjectToken, "token", 't', "Project token", "STRING")
	optset.FlagLong(&labels, "labels", 'l', "Extra labels in addition to the default", "STRING")
	optset.Parse()

	args = optset.Args()
	switch len(args) {
	case 0:
		if optset.Entity.Type == EntityRepo {
			if n := strings.Index(optset.Entity.Name, `/`); n != -1 {
				ProjectName = optset.Entity.Name[n+1:]
			}
		}

		if ProjectName == "" {
			if ProjectUrl == "" {
				log.Fatalf("either --url or PROJECTNAME must be given; try `%s --help' for assistance", optset.Command)
			}
			ProjectName = filepath.Base(ProjectUrl)
		}

	case 1:
		ProjectName = args[0]

	default:
		log.Fatalf("too many arguments; try `%s --help' for assistance", optset.Command)
	}

	if optset.Entity.Type == EntityRepo {
		//FIXME: is that needed?
		if n := strings.Index(optset.Entity.Name, `/`); n == -1 {
			optset.Entity.Name += `/` + ProjectName
		} else if optset.Entity.Name[n+1:] != ProjectName {
			log.Fatal("repository suffix doesn't match project name")
		}
	}

	if ProjectUrl == "" {
		ProjectUrl = optset.Entity.ProjectURL(ProjectName)
	}

	if ProjectToken == "" {
		var err error
		ProjectToken, err = GetToken(optset.Entity.TokenKey(RegistrationToken, ProjectName))
		if err != nil {
			log.Fatal(err)
		}
	}

	pc, err := ParsePiesConfig(config.PiesConfigFile)
	if err != nil {
		log.Panic(err)
	}

	n := 0
	r, ok := pc.Runners[ProjectName]
	if ok {
		n = r[len(r)-1].Num + 1
	}

	name := ProjectName + `_` + strconv.Itoa(n)
	// FIXME: check if dirname exists

	if err := InstallToDir(name, ProjectUrl, ProjectToken, labels); err != nil {
		log.Fatal(err)
	}

	if err := pc.AddRunner(name); err != nil {
		log.Fatal(err)
	}

	if err := pc.Save(); err != nil {
		log.Fatal(err)
	}

	if err := PiesReloadConfig(pc.ControlURL); err != nil {
		log.Fatalf("Pies configuration updated, but pies not reloaded: %v", err)
	}
}

func RemoveRunner(name, dirname, token string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("can't get cwd: %v", err)
	}
	defer os.Chdir(cwd)

	if err := os.Chdir(dirname); err != nil {
		return fmt.Errorf("can't chdir to %s: %v", dirname, err)
	}

	cmd := exec.Command("./config.sh", "remove", "--token", token)

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Error removing %s: %v", name, err)
	}

	if err := os.Chdir(cwd); err != nil {
		return fmt.Errorf("can't chdir to %s: %v", cwd, err)
	}

	if err := os.RemoveAll(dirname); err != nil {
		return fmt.Errorf("failed to remove %s: %v", dirname, err)
	}
	return nil
}

const (
	EntityEnterprise = iota
	EntityOrg
	EntityRepo

	RemoveToken = `remove-token`
	RegistrationToken = `registration-token`
)

var GHEntityPrefix = []string{
	`/enterprises/`,
	`/orgs/`,
	`/repos/`,
}

func (ent entityValue) PATKey() string {
	return GHEntityPrefix[ent.Type] + ent.Name
}

func (ent entityValue) TokenKey(kind, project string) string {
	//FIXME
	if ent.Type == EntityRepo {
		return ent.PATKey() + `/actions/runners/` + kind
	} else {
		return ent.PATKey() + `/` + project + `/actions/runners/` + kind
	}
}

func (ent entityValue) ProjectURL(name string) string {
	//FIXME
	return `https://github.com/` + ent.Name + `/` + name
}

type GHToken struct {
	Token string         `json:"token"`
	ExpiresAt time.Time  `json:"expires_at"`
}

func DeleteAction(args []string) {
	ReadConfig()
	FinalizeConfig()
	optset := NewEntityOptset(args)
	optset.SetParameters("PROJECTNAME [NUMBER]")
	var (
		keep bool
		force bool
		token string
	)
	optset.FlagLong(&keep, "keep", 'k', "Keep the configured runner directory")
	optset.FlagLong(&force, "force", 'f', "Force removal of the runner directory")
	optset.FlagLong(&token, "token", 0, "Removal token", "STRING")
	optset.Parse()

	args = optset.Args()
	var (
		projectName string
		runnerNum int
	)
	switch len(args) {
	case 2:
		projectName = args[0]
		n, err := strconv.Atoi(args[1])
		if err != nil {
			log.Fatalf("bad runner number: %s", args[1])
		}
		runnerNum = n

	case 1:
		projectName = args[0]
		runnerNum = -1	// remove last

	default:
		log.Fatalf("wrong number of arguments; try `%s --help' for assistance", optset.Command)
	}

	if optset.Entity.Type == EntityRepo {
		if s := strings.TrimSuffix(optset.Entity.Name, `/` + projectName); s != optset.Entity.Name {
			// ok
		} else if n := strings.Index(optset.Entity.Name, `/`); n == -1 {
			optset.Entity.Name += `/` + projectName
		}
	}

	if force && keep {
		log.Fatal("--force and --keep can't be used together")
	}

	if token == "" && !keep {
		var err error
		token, err = GetToken(optset.Entity.TokenKey(RemoveToken, projectName))
		if err != nil {
			log.Fatal(err)
		}
	}

	pc, err := ParsePiesConfig(config.PiesConfigFile)
	if err != nil {
		log.Panic(err)
	}

	r, ok := pc.Runners[projectName]
	if !ok {
		log.Fatalf("found no runners for %s", projectName)
	}

	var i int

	if runnerNum == -1 {
		i = len(r) - 1
		runnerNum = r[i].Num
		fmt.Printf("Removing runner %s_%d\n", projectName, runnerNum)
	} else {
		i = sort.Search(len(r), func(i int) bool { return r[i].Num >= runnerNum })
		if !(i < len(r) && r[i].Num == runnerNum) {
			log.Fatalf("%s: no runner %d", projectName, runnerNum)
		}
	}

	if token != "" {
		if err := RemoveRunner(projectName + `_` + strconv.Itoa(runnerNum), r[i].Dir, token); err != nil {
			log.Printf("failed to remove runner: %v", err)
			if force {
				log.Printf("continuing anyway")
			}
			os.Exit(1)
		}
	}

	pc.Tokens = append(pc.Tokens[:r[i].TokenStart], pc.Tokens[r[i].TokenEnd+1:]...)
	if err := pc.Save(); err != nil {
		log.Fatal(err)
	}
	if err := PiesReloadConfig(pc.ControlURL); err != nil {
		log.Fatalf("Pies configuration updated, but pies not reloaded: %v", err)
	}

}

func CheckConfigAction(args []string) {
	optset := NewOptset(args)
	optset.SetParameters("")
	list := false
	optset.FlagLong(&list, "list", 'l', "Show the configuration")
	optset.Parse()

	if len(optset.Args()) != 0 {
		log.Fatalf("too many arguments; try `%s --help' for assistance", optset.Command)
	}

	if ok, filename := ReadConfig(); ok {
		fmt.Printf("Using configuration file %s\n", filename)
	} else {
		fmt.Println("Using built-in configuration defaults")
	}

	if list {
		Annotate(&config, os.Stdout)
		// yml, err := yaml.Marshal(&config)
		// if err != nil {
		//	log.Panic(err)
		// }
		// fmt.Println(string(yml))
	}

	if ! VerifyStruct(&config, true) {
		os.Exit(1)
	}
	os.Exit(0)
}

func StatusAction(args []string) {
	optset := NewOptset(args)
	optset.SetParameters("")
	verbose := false
	optset.FlagLong(&verbose, "verbose", 'v', "Increase verbosity")
	optset.Parse()

	if len(optset.Args()) != 0 {
		log.Fatalf("too many arguments; try `%s --help' for assistance", optset.Command)
	}

	if ok, filename := ReadConfig(); ok {
		fmt.Printf("Using configuration file %s\n", filename)
	} else {
		fmt.Println("Using built-in configuration defaults")
	}

	if VerifyStruct(&config, verbose) {
		fmt.Println("Configuration file passed syntax check")
	} else {
		os.Exit(1)
	}

	pc, err := ParsePiesConfig(config.PiesConfigFile)
	if err != nil {
		log.Panic(err)
	}

	if err, info := GetPiesInstanceInfo(pc.ControlURL); err == nil {
		fmt.Printf("%s %s running with PID %d\n", info.PackageName, info.Version, info.PID)
	} else {
		fmt.Println(err)
	}

	if err, info := GetPiesComponentInfo(pc.ControlURL); err == nil {
		if n := len(info); n == 0 {
			fmt.Println("No runners active")
		} else {
			fmt.Printf("%d runners active\n", n)
		}
	}
}

func PiesStart() {
	fmt.Println("Starting GNU pies")
	cmd := exec.Command(config.Pies, "--config-file", config.PiesConfigFile)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("Can't start %s: %v", config.Pies, err)
	}
}

func StartAction(args []string) {
	optset := NewOptset(args)
	optset.SetParameters("")
	optset.Parse()

	if len(optset.Args()) != 0 {
		log.Fatalf("too many arguments; try `%s --help' for assistance", optset.Command)
	}

	ReadConfig()

	if ! VerifyStruct(&config, false) {
		log.Fatalf("configuration fails sanity checking; run `%s configcheck' for more info", os.Args[0])
	}

	if pc, err := ParsePiesConfig(config.PiesConfigFile); err == nil {
		if err, _ := GetPiesInstanceInfo(pc.ControlURL); err == nil {
			log.Fatalf("GNU pies supervisor is running; run `%s status` for more info", os.Args[0])
		}
	} else {
		log.Fatal(err)
	}

	PiesStart()
}

func StopAction(args []string) {
	optset := NewOptset(args)
	optset.SetParameters("")
	optset.Parse()

	if len(optset.Args()) != 0 {
		log.Fatalf("too many arguments; try `%s --help' for assistance", optset.Command)
	}

	ReadConfig()
	pc, err := ParsePiesConfig(config.PiesConfigFile)
	if err != nil {
		log.Panic(err)
	}

	if err, _ := GetPiesInstanceInfo(pc.ControlURL); err != nil {
		log.Fatal("No running pies instance found")
	}

	if err := PiesStopInstance(pc.ControlURL); err != nil {
		log.Fatal(err)
	}
	fmt.Println("GNU pies stopped")
}

func RestartAction(args []string) {
	optset := NewOptset(args)
	optset.SetParameters("")
	optset.Parse()

	if len(optset.Args()) != 0 {
		log.Fatalf("too many arguments; try `%s --help' for assistance", optset.Command)
	}

	ReadConfig()
	pc, err := ParsePiesConfig(config.PiesConfigFile)
	if err != nil {
		log.Panic(err)
	}

	if err, _ := GetPiesInstanceInfo(pc.ControlURL); err == nil {
		if err := PiesStopInstance(pc.ControlURL); err != nil {
			log.Fatal(err)
		}
		fmt.Println("GNU pies restarted")
	} else {
		PiesStart()
	}
}

var (
	PiesVersionRx = regexp.MustCompile(`^pies\s+\(GNU Pies\)\s+(\d+(:?\.\d+){1,2})(\S+)?`)
	PiesVersionMin = `1.7.92`
)

func CheckPiesCommand(exe string) error {
	out, err := exec.Command(exe, "--version").Output()
	if err != nil {
		return err
	}

	if m := PiesVersionRx.FindStringSubmatch(string(out)); m != nil {
		if semver.Compare(semver.Canonical(`v`+m[1]), semver.Canonical(`v`+PiesVersionMin)) < 0 {
			return fmt.Errorf("version too old: %s", m[1])
		}
	} else {
		return errors.New("can't determine GNU pies version")
	}
	return nil
}

func findStringPrefix(a []string, p string) int {
	p += `:`
	for i, s := range a {
		if strings.HasPrefix(s, p) {
			return i
		}
	}
	return -1
}

func NormalizeRel(obj interface{}) {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		log.Panic("Passed object has unsupported type: %s", v.Kind())
	}
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		f := t.Field(i)
		relname := f.Tag.Get(`rel`)
		if relname != "" {
			if relv := v.FieldByName(relname); !relv.IsZero() {
				basepath := relv.Interface().(string)
				path := v.Field(i).Interface().(string)
				if s, err := filepath.Rel(basepath, path); err == nil {
					v.Field(i).SetString(s)
				}
			}
		}
	}
}

func Annotate(obj interface{}, wr io.Writer) error {
	b, err := yaml.Marshal(obj)
	if err != nil {
		return err
	}
	astr := strings.Split(string(b), "\n")

	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return fmt.Errorf("Passed object has unsupported type: %s", v.Kind())
	}
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		f := t.Field(i)
		name := f.Tag.Get(`yaml`)
		if name == "" {
			continue
		}
		n := strings.IndexByte(name, byte(','))
		if n != -1 {
			name = name[0:n]
		}
		if name == "" || name == "-" {
			continue
		}
		d := f.Tag.Get(`rem`)
		j := findStringPrefix(astr, name)
		if j != -1 {
			astr = append(astr[:j+1], astr[j:]...)
			astr[j] = `# ` + d
		}
	}
	fmt.Fprintln(wr, strings.Join(astr, "\n"))
	return nil
}

func SetupAction(args []string) {
	optset := NewOptset(args)
	optset.SetParameters("")
	make_config := false
	optset.FlagLong(&make_config, "make-config", 0, "Create ghb.conf configuration file")
	optset.Parse()

	args = optset.Args()
	if len(args) != 1 {
		log.Fatalf("required argument (your GitHub user name) missing; try `%s --help' for assistance", optset.Command)
	}

	ReadConfig()
	if VerifyStruct(&config, false) {
		log.Printf("ghb appears to be set up already")
		if pc, err := ParsePiesConfig(config.PiesConfigFile); err == nil {
			if err, info := GetPiesInstanceInfo(pc.ControlURL); err == nil {
				log.Printf("%s %s running with PID %d\n", info.PackageName, info.Version, info.PID)
			}
		}
		os.Exit(1)
	}

	FinalizeConfig()
	if ! VerifyStruct(&config, false) {
		log.Fatalf("configuration fails sanity checking; run `%s configcheck' for more info", os.Args[0])
	}

	if make_config {
		filename := filepath.Join(GetHomeDir(), `ghb.conf`)
		file, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			log.Fatal("can't create %s: %v", filename, err)
		}
		NormalizeRel(&config)
		Annotate(&config, file)
		file.Close()
	}

	PiesStart()
	fmt.Printf("Setup finished.  Run `%s add' to add new runners.\n", os.Args[0])
}

type timeValue time.Time

func (tv *timeValue) Set(value string, opt getopt.Option) error {
	if s := strings.TrimPrefix(value, `+`); s != value {
		if d, err := time.ParseDuration(s); err == nil {
			*tv = timeValue(time.Now().Add(d))
		} else {
			return err
		}
	} else if t, err := time.Parse("2006-01-02 15:04:05", value); err != nil {
		return err
	} else {
		*tv = timeValue(t)
	}
	return nil
}

func (tv *timeValue) String() string {
	return time.Time(*tv).String()
}

func (tok GHToken) Print() {
	fmt.Printf("Token: %s\n", tok.Token)
	if time.Now().Before(tok.ExpiresAt) {
		if t, err := tok.ExpiresAt.MarshalText(); err == nil {
			fmt.Printf("Expires at: %s\n", string(t))
		}
	} else {
		if t, err := tok.ExpiresAt.MarshalText(); err == nil {
			fmt.Printf("Expired (at: %s)\n", string(t))
		} else {
			fmt.Printf("Expired!\n")
		}
	}
}

func PatAction(args []string) {
	ReadConfig()
	optset := NewEntityOptset(args)
	optset.SetParameters("")
	var (
		expiration timeValue
		token string
		delete bool
		all bool
	)
	optset.FlagLong(&token, "set", 's', "Set new PAT", "STRING")
	optset.FlagLong(&expiration, "expires", 'e', "STRING")
	optset.FlagLong(&delete, "delete", 'd', "Delete PAT")
	optset.FlagLong(&all, "all", 'a', "List all keys for the given entity")
	optset.Parse()

	if delete && token != "" {
		log.Fatal("--delete and --set cannot be used together")
	}

	if delete {
		if err := DeleteToken(optset.Entity.PATKey()); err != nil {
			log.Fatal(err)
		}
	} else if token == "" {
		if tok, err := FetchRawToken(optset.Entity.PATKey()); err == nil {
			tok.Print()
			if all {
				if next, err := PrefixIterator(optset.Entity.PATKey()); err == nil {
					for key, tok, err := next(); err == nil; key, tok, err = next() {
						fmt.Printf("\nName: %s\n", key)
						tok.Print()
					}
				}
			}
		} else {
			log.Fatal(err)
		}
	} else {
		exptime := time.Time(expiration)
		if exptime.IsZero() {
			exptime = time.Now().Add(time.Hour * 24 * 7)
		}
		tok := GHToken{Token: token, ExpiresAt: exptime}
		if err := SaveToken(optset.Entity.PATKey(), tok); err != nil {
			log.Fatal(err)
		}
	}
}

func main() {
	log.SetPrefix(filepath.Base(os.Args[0]) + ": ")
	log.SetFlags(log.Lmsgprefix)

	getopt.SetProgram(filepath.Base(os.Args[0]))
	getopt.SetParameters("COMMAND [OPTIONS]")
	getopt.Parse()

	args := getopt.Args()

	actions = map[string]Action{
		"add":     Action{Action: AddAction,
				  Help: "Add runner"},
		"delete":  Action{Action: DeleteAction,
				  Help: "Delete a runner"},
		"list":    Action{Action: ListAction,
				  Help: "List existing runners"},
		"configcheck": Action{Action: CheckConfigAction,
				      Help: "Check current configuration"},
		"status":  Action{Action: StatusAction,
				  Help: "Check ghb system status"},
		"setup":   Action{Action: SetupAction,
				  Help: "Set up GHB subsystem"},
		"stop":    Action{Action: StopAction,
				  Help: "Stop GNU pies supervisor"},
		"start":   Action{Action: StartAction,
				  Help: "Start GNU pies supervisor"},
		"restart": Action{Action: RestartAction,
				  Help: "Restart GNU pies supervisor"},
		"help":    Action{Action: HelpAction,
				  Help: "Show a short help summary"},
		"pat":     Action{Action: PatAction,
                                  Help: "Manage private access keys"},
//		"try":    Action{Action: TryAction, Help: "Guess what..."},
	}

	if len(os.Args) == 1 {
		log.Fatalf("command missing; try `%s help' for assistance", filepath.Base(os.Args[0]))
	}

	if act, ok := actions[args[0]]; ok {
		act.Action(args)
		os.Exit(0)
	}

	log.Fatalf("unrecognized action; try `%s help' for assistance", filepath.Base(os.Args[0]))
}
