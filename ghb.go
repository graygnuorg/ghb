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
	"text/template"
	"context"
	"sort"
	"github.com/pborman/getopt/v2"
	"gopkg.in/yaml.v2"
	"github.com/graygnuorg/go-gdbm"
)

type Config struct {
	Url string                `yaml:"url"`
	RootDir string            `yaml:"root_dir"`
	RunnersDir string         `yaml:"runners_dir"`
	CacheDir string           `yaml:"cache_dir"`
	Tar string                `yaml:"tar"`
	PiesConfigFile string     `yaml:"pies_config_file"`
	ComponentTemplate string  `yaml:"component_template"`
}

var config = Config{
	Url: `https://github.com/actions/runner/releases/download/v2.294.0/actions-runner-linux-x64-2.294.0.tar.gz`,
	RunnersDir: ``,
	CacheDir: ``,
	Tar: `tar`,
	PiesConfigFile: ``,
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

func ReadConfig() {
	var config_file_name string
	env_name := os.Getenv("GHB_CONFIG")
	if env_name == "" {
		config_file_name = filepath.Join(GetHomeDir(), `ghb.conf`)
	} else {
		config_file_name = env_name
	}
	content, err := ioutil.ReadFile(config_file_name)
	if err == nil {
		err = yaml.Unmarshal([]byte(content), &config)
		if err != nil {
			log.Fatalf("%s: %v", config_file_name, err)
		}
	} else if env_name == "" && errors.Is(err, os.ErrNotExist) {
		// OK, default config is not required to exist
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
		"--unattended", "--replace",
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

func ExpandTemplate(runnerName string) (string, error) {
	tmpl, err := template.New("component").Funcs(template.FuncMap{
		"RunnerName": func () string { return runnerName },
		"Config": func () *Config { return &config },
	}).Parse(config.ComponentTemplate)
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
		m := runnerNameRx.FindStringSubmatch(t.Text)
		if m != nil {
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
	text, err := ExpandTemplate(name)
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

type PiesReloadResponse struct {
	Status string
	Message string
	//Parser_messages []string
}

var allIPRx = regexp.MustCompile(`^(0\.0\.0\.0)?(:.+)`)

func PiesReloadConfig(controlURL *url.URL) error {
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

	rurl := &url.URL{Scheme: "http", Path: `/conf/runtime`}
	if controlURL.Scheme == "inet" {
		rurl.Host = controlURL.Host
	} else {
		rurl.Host = "localhost"
	}

	req, err := http.NewRequest(http.MethodPut, rurl.String(), nil)
	if err != nil {
		return fmt.Errorf("can't create HTTP request: %v", err)
	}
	resp, err := clt.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			log.Printf("Pies not running?")
			return nil
		} else {
			return fmt.Errorf("can't query: %v", err)
		}
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("can't read response: %v", err)
	}

	var rresp PiesReloadResponse
	err = json.Unmarshal(body, &rresp)
	if err != nil {
		return fmt.Errorf("can't parse response %s: %v", string(body), err)
	}

	if rresp.Status == "OK" {
		fmt.Println("Pies reload successful")
	} else {
		return fmt.Errorf("Pies reload failed: %s %s", rresp.Status, rresp.Message)
	}

	return nil
}

func SaveToken(name, token string) error {
	dbname := filepath.Join(config.CacheDir, `token.db`)
	db, err := gdbm.Open(dbname, gdbm.ModeWrcreat)
	if err != nil {
		return fmt.Errorf("can't open database file %s for update: %v", dbname, err)
	}
	defer db.Close()
	if err := db.Store([]byte(name), []byte(token), true); err != nil {
		return fmt.Errorf("can't store key %s: %v", name, err)
	}
	return nil
}

func FetchToken(name string) (string, error) {
	dbname := filepath.Join(config.CacheDir, `token.db`)
	db, err := gdbm.Open(dbname, gdbm.ModeReader)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = gdbm.ErrItemNotFound
		}
		return "", err
	}
	defer db.Close()
	if ret, err := db.Fetch([]byte(name)); err == nil {
		return string(ret), nil
	} else {
		return "", err
	}
}

func DeleteToken(name string) error {
	dbname := filepath.Join(config.CacheDir, `token.db`)
	db, err := gdbm.Open(dbname, gdbm.ModeWrcreat)
	if err != nil {
		return fmt.Errorf("can't open database file %s for update: %v", dbname, err)
	}
	defer db.Close()
	return db.Delete([]byte(name))
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
	FinalizeConfig()
	optset := NewOptset(args)
	optset.SetParameters("[PROJECTNAME]")
	var (
		ProjectUrl string
		ProjectToken string
		ProjectName string
	)
	optset.FlagLong(&ProjectUrl, "url", 0, "Project URL (mandatory)")
	optset.FlagLong(&ProjectToken, "token", 0, "Project token (mandatory)")
	labels := ""
	optset.FlagLong(&labels, "labels", 'l', "Extra labels in addition to the default")
	optset.Parse()

	args = optset.Args()
	switch len(args) {
	case 0:
		// ok
	case 1:
		ProjectName = args[0]
	default:
		log.Fatalf("too many arguments; try `%s --help' for assistance", optset.Command)
	}

	pc, err := ParsePiesConfig(config.PiesConfigFile)
	if err != nil {
		log.Panic(err)
	}

	if ProjectUrl == "" || ProjectToken == "" {
		log.Fatal("both --url and --token are mandatory")
	}

	if ProjectName == "" {
		ProjectName = filepath.Base(ProjectUrl)
	}

	n := 0
	r, ok := pc.Runners[ProjectName]
	if ok {
		n = r[len(r)-1].Num + 1
	}

	name := ProjectName + `_` + strconv.Itoa(n)
	// FIXME: check if dirname exists

	if err := SaveToken(name, ProjectToken); err != nil {
		log.Printf("failed to save token for %s: %v", name, err)
		log.Printf("continuing anyway")
	}

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

func RemoveRunner(name, dirname string) {
	token, err := FetchToken(name)
	if err != nil {
		log.Printf("can't find token for %s: %v", name, err)
		log.Printf("directory %s left in place")
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		log.Printf("Can't get cwd: %v", err)
		return
	}
	defer os.Chdir(cwd)

	err = os.Chdir(dirname)
	if err != nil {
		log.Printf("can't chdir to %s: %v", dirname, err)
	}

	cmd := exec.Command("./config.sh", "remove", "--token", token)

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("Error removing %s: %v", name, err)
		return
	}

	err = os.Chdir(cwd)
	if err != nil {
		log.Printf("can't chdir to %s: %v", cwd, err)
	}

	if err = DeleteToken(name); err != nil {
		log.Printf("failed to delete token for %s: %v", name, err)
	}

	err = os.RemoveAll(dirname)
	if err != nil {
		log.Printf("failed to remove %s: %v", dirname, err)
	}
}

func DeleteAction(args []string) {
	FinalizeConfig()
	optset := NewOptset(args)
	optset.SetParameters("PROJECTNAME [NUMBER]")
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
		m := runnerNameRx.FindStringSubmatch(args[0])
		if m != nil {
			if n, err := strconv.Atoi(m[2]); err == nil {
				projectName = m[1]
				runnerNum = n
			} else {
				log.Fatalf("bad runner number: %s", m[2])
			}
		} else {
			log.Fatalf("unsupported runner name: %s", args[0])
		}

	default:
		log.Fatalf("wrong number of arguments; try `%s --help' for assistance", optset.Command)
	}

	pc, err := ParsePiesConfig(config.PiesConfigFile)
	if err != nil {
		log.Panic(err)
	}

	r, ok := pc.Runners[projectName]
	if !ok {
		log.Fatalf("found no runners for %s", projectName)
	}

	i := sort.Search(len(r), func(i int) bool { return r[i].Num >= runnerNum })
	if i < len(r) && r[i].Num == runnerNum {
		pc.Tokens = append(pc.Tokens[:r[i].TokenStart], pc.Tokens[r[i].TokenEnd+1:]...)
		if err := pc.Save(); err != nil {
			log.Fatal(err)
		}
		if err := PiesReloadConfig(pc.ControlURL); err != nil {
			log.Fatalf("Pies configuration updated, but pies not reloaded: %v", err)
		}

		RemoveRunner(projectName + `_` + strconv.Itoa(runnerNum), r[i].Dir)
	} else {
		log.Fatalf("%s: no runner %d", projectName, runnerNum)
	}
}

func CheckConfigAction(args []string) {
	optset := NewOptset(args)
	optset.SetParameters("")
	finalize := false
	optset.FlagLong(&finalize, "finalize", 'f', "Finalize the configuration")
	optset.Parse()

	if len(optset.Args()) != 0 {
		log.Fatalf("too many arguments; try `%s --help' for assistance", optset.Command)
	}

	yml, err := yaml.Marshal(&config)
	if err != nil {
		log.Panic(err)
	}
	fmt.Println(string(yml))

	if finalize {
		FinalizeConfig()
	}
}

func main() {
	log.SetPrefix(filepath.Base(os.Args[0]) + ": ")
	log.SetFlags(log.Lmsgprefix)

	ReadConfig()

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
		"check":   Action{Action: CheckConfigAction,
				  Help: "Check current configuration"},
		"help":    Action{Action: HelpAction,
				  Help: "Show a short help summary"},
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
