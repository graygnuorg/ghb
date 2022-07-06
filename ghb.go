package main

import (
	"os"
	"net/http"
	"io"
	"fmt"
	"path/filepath"
	"os/exec"
	"log"
	"strings"
	"unicode"
	"unicode/utf8"
	"encoding/json"
	"io/ioutil"
	"regexp"
	"strconv"
	"errors"
	"net"
	"net/url"
	"text/template"
	"context"
	"github.com/pborman/getopt/v2"
	//"github.com/graygnuorg/go-gdbm"
)

type Config struct {
	Url string
	RootDir string
	CacheDir string
	Tar string
	PiesConfigFile string
	ComponentTemplate string
}

var config = Config{
	Url: `https://github.com/actions/runner/releases/download/v2.294.0/actions-runner-linux-x64-2.294.0.tar.gz`,
	RootDir: `/tmp/GHB`,
	CacheDir: `/tmp/ghb.cache`,
	Tar: `tar`,
	PiesConfigFile: `/home/gray/exp/ghb/pies.conf`,
	ComponentTemplate: `component {{ RunnerName }} {
        mode respawn;
        chdir "{{ Config.RootDir }}/{{ RunnerName }}";
        stderr syslog daemon.err;
        stdout syslog daemon.info;
	flags siggroup;
        command "./run.sh";
}
`,
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

func GetArchiveFile() (filename string, e error) {
	st, err := os.Stat(config.CacheDir)
	switch {
	case os.IsNotExist(err):
		err := os.MkdirAll(config.CacheDir, 0750)
		if err != nil {
			e = fmt.Errorf("Can't cache dir %s: %v", config.CacheDir, err)
			return
		}
	case err == nil:
		if !st.IsDir() {
			e = fmt.Errorf("%s exists, but is not a directory", config.CacheDir)
			return
		}

	default:
		e = fmt.Errorf("Can't stat %s: %v", config.CacheDir, err)
		return
	}

	filename = filepath.Join(config.CacheDir, filepath.Base(config.Url))
	_, err = os.Stat(filename)
	switch {
	case err == nil:
		fmt.Printf("Using cached copy %s\n", filename)
		break
	case os.IsNotExist(err):
		fmt.Printf("Downloading %s\n", config.Url)
		e = download(filename)
	default:
		e = fmt.Errorf("Can't stat %s: %v", filename, err)
	}

	return
}

func InstallToDir(projectName, projectUrl, projectToken string) error {
	arc, err := GetArchiveFile()
	if err != nil {
		return err
	}

	dirname := filepath.Join(config.RootDir, projectName)
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
	cmd = exec.Command("./config.sh", "--name", name, "--url", projectUrl, "--token", projectToken, "--unattended", "--replace")
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

type Token struct {
	Type int
	Text string
	offset int
	lineNo int
}

func (t *Token) EOF() bool {
	return t.Type == TokenEOF
}

func (t *Token) IsText() bool {
	return t.Type == TokenWord || t.Type == TokenString
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

func (l *Lexer) Error(text string) error {
	return fmt.Errorf("%s:%d.%d: %s", l.fileName, l.lineNo + 1, l.offset - l.lineOffset + 1, text)
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
	offset := l.offset
	lineNo := l.lineNo
	for l.ch != eof && unicode.IsSpace(l.ch) {
		sb.WriteRune(l.ch)
		_, err = l.NextChar()
		if err != nil {
			return
		}
	}
	token = &Token{Type: TokenWS, Text: sb.String(), offset: offset, lineNo: lineNo}
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
	offset := l.offset
	lineNo := l.lineNo
	for IsWord(l.ch) {
		sb.WriteRune(l.ch)
		_, err = l.NextChar()
		if err != nil {
			return
		}
	}
	token = &Token{Type: TokenWord, Text: sb.String(), offset: offset, lineNo: lineNo}
	return
}

func (l *Lexer) scanString() (token *Token, err error) {
	var sb strings.Builder
	offset := l.offset
	lineNo := l.lineNo
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
 	token = &Token{Type: TokenString, Text: sb.String(), offset: offset, lineNo: lineNo}
	return
}
	
func (l *Lexer) scanInlineComment(r rune) (token *Token, err error) {
	var sb strings.Builder
	if r != 0 {
		sb.WriteRune(r)
	}
	offset := l.offset
	lineNo := l.lineNo
	for l.ch != eof && l.ch != '\n' {
		sb.WriteRune(l.ch)
		_, err = l.NextChar()
		if err != nil {
			return
		}
	}
 	token = &Token{Type: TokenComment, Text: sb.String(), offset: offset, lineNo: lineNo}
	return
}

func (l *Lexer) scanComment() (token *Token, err error) {
	var sb strings.Builder
	offset := l.offset
	lineNo := l.lineNo
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
 	token = &Token{Type: TokenComment, Text: sb.String(), offset: offset, lineNo: lineNo}
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
		token = &Token{Type: TokenEOF, offset: l.offset, lineNo: l.lineNo}

	case r == '#':
		token, err = l.scanInlineComment(0)

	case r == '/':
		var r1 rune
		offset := l.offset
		lineNo := l.lineNo
		r1, err = l.NextChar()
		if err != nil {
			return
		}
		if r1 == '*' {
			token, err = l.scanComment()
		} else if r1 == '/' {
			token, err = l.scanInlineComment('/')
		} else {
			token = &Token{Type: TokenPunct, Text: string(r), offset: offset, lineNo: lineNo}
		}

	case r == '"':
		token, err = l.scanString()
		
	case unicode.IsPunct(r):
		token = &Token{Type: TokenPunct, Text: string(r), offset: l.offset, lineNo: l.lineNo}
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
		if !(token.Type == TokenWS || token.Type == TokenComment) {
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
		if t.EOF() {
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
		if t.EOF() {
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

type PiesConfig struct {
	FileName string
	ControlURL *url.URL
	Runners map[string]int
	Tokens []*Token
}

func (pc *PiesConfig) ParseControl(l *Lexer) error {
	t, err := l.NextNWSToken()
	if err != nil {
		return err
	}
	if t.EOF() {
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
				log.Printf("%s:%d: can't parse URL: %v", l.fileName, t.lineNo + 1, err)
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
	t, err := l.NextNWSToken()
	if err != nil {
		return err
	}
	if t.EOF() {
		return nil
	}

	if t.IsText() {
		m := runnerNameRx.FindStringSubmatch(t.Text)
		if m != nil {
			n, err := strconv.Atoi(m[2])
			if err == nil {
				if val, ok := pc.Runners[m[1]]; ok {
					if val < n {
						pc.Runners[m[1]] = n
					}
				} else {
					pc.Runners[m[1]] = n
				}
			}
		}
	}
	return l.SkipStatement()
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
	pc := &PiesConfig{FileName: filename, Runners: make(map[string]int)}
	
	l, err := LexerNew(filename)
	if err != nil {
		return nil, err
	}

	for {
		t, err := l.NextNWSToken()
		if err != nil {
			return nil, err
		}
		if t.EOF() {
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
	// 	t, err := l.NextToken()
	// 	if err != nil {
	// 		return err
	// 	}
	// 	if t.EOF() {
	// 		break
	// 	}
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

func PiesReloadConfig(controlURL *url.URL) error {
	clt := http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				switch controlURL.Scheme {
				case `local`, `file`, `unix`:
					return net.Dial(`unix`, controlURL.Path)
				case `inet`:
					return net.Dial(`tcp`, controlURL.Host)
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
		return fmt.Errorf("can't query: %v", err)
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


func main() {
	log.SetPrefix(filepath.Base(os.Args[0]) + ": ")
	log.SetFlags(log.Lmsgprefix)

	getopt.SetProgram(filepath.Base(os.Args[0]))
	getopt.SetParameters("[PROJECTNAME]")
	var (
		ProjectUrl string
		ProjectToken string
		ProjectName string
	)
	getopt.FlagLong(&ProjectUrl, "url", 0, "Project URL (mandatory)")
	getopt.FlagLong(&ProjectToken, "token", 0, "Project token (mandatory)")
	getopt.Parse()

	args := getopt.Args()
	switch len(args) {
	case 0:
		// ok
	case 1:
		ProjectName = args[0]
	default:
		log.Fatalf("too many arguments; try `%s help' for assistance", filepath.Base(os.Args[0]))
	}

	pc, err := ParsePiesConfig(config.PiesConfigFile)
	if err != nil {
		panic(err)//FIXME
	}

	if ProjectUrl == "" || ProjectToken == "" {
		log.Fatal("both --url and --token are mandatory")
	}

	if ProjectName == "" {
		ProjectName = filepath.Base(ProjectUrl)
	}

	n, ok := pc.Runners[ProjectName]
	if ok {
		n += 1
	} else {
		n = 0
	}

	name := ProjectName + `_` + strconv.Itoa(n)
	// FIXME: check if dirname exists

	if err := InstallToDir(name, ProjectUrl, ProjectToken); err != nil {
		panic(err)
	}

	if err := pc.AddRunner(name); err != nil {
		panic(err)
	}
	
	if err := pc.Save(); err != nil {
		panic(err)
	}

	if err := PiesReloadConfig(pc.ControlURL); err != nil {
		panic(err)
	}
	os.Exit(0)
}
