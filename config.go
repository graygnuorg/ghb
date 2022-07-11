// THE BEER-WARE LICENSE" (Revision 42):
// <gray@gnu.org> wrote this file.  As long as you retain this notice you
// can do whatever you want with this stuff. If we meet some day, and you
// think this stuff is worth it, you can buy me a beer in return.

package main

import (
	"os"
	"os/user"
	"os/exec"
	"fmt"
	"path/filepath"
	"io"
	"io/ioutil"
	"errors"
	"log"
	"reflect"
	"text/template"
	"strings"
	"gopkg.in/yaml.v2"
)

type Config struct {
	RootDir string            `yaml:"root_dir" rem:"Root directory" verify:"dir_exist"`
	RunnersDir string         `yaml:"runners_dir" rem:"Directory for storing runners" verify:"dir_exist"  rel:"RootDir"`
	CacheDir string           `yaml:"cache_dir" rem:"Cache directory" verify:"dir_exist" rel:"RootDir"`
	Tar string                `yaml:"tar" rem:"Tar binary" verify:"exe"`
	Pies string               `yaml:"pies" rem:"Pies binary" verify:"pies_version"`
	PiesConfigFile string     `yaml:"pies_config_file" rem:"Pies configuration file name" verify:"pies_config" rel:"RootDir"`
	ComponentTemplate string  `yaml:"component_template" rem:"Template for runner components" verify:"component_template"`
}

var config = Config{
	RunnersDir: ``,
	CacheDir: ``,
	Tar: `tar`,
	Pies: `pies`,
	PiesConfigFile: ``,
	// FIXME: Make sure the lines in the literal below are indented using spaces, not tabs.
	// This way yaml marshaller prints the value in readable form.
        ComponentTemplate: `component "{{ RunnerName }}" {
        mode respawn;
        chdir "{{ Config.RunnersDir }}/{{ RunnerName }}";
        stderr syslog daemon.err;
        stdout syslog daemon.info;
        flags siggroup;
        command "./run.sh";
}
`,
}

var DefaultPiesPort = "8073"

var PiesConfigStub = `
pidfile {{ Config.RootDir }}/pies.pid;
control {
	socket "inet://127.0.0.1:{{ Port }}";
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
		"Port": func () string { return DefaultPiesPort },
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
