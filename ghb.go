// THE BEER-WARE LICENSE" (Revision 42):
// <gray@gnu.org> wrote this file.  As long as you retain this notice you
// can do whatever you want with this stuff. If we meet some day, and you
// think this stuff is worth it, you can buy me a beer in return.

package main

import (
	"os"
	"os/exec"
	"fmt"
	"path/filepath"
	"log"
	"strings"
	"regexp"
	"strconv"
	"errors"
	"time"
	"text/template"
	"sort"
	"golang.org/x/mod/semver"
	"github.com/pborman/getopt/v2"
//?	"gopkg.in/yaml.v2"
	"runtime"
)

func InstallToDir(arc, projectName, projectUrl, projectToken, labels string) error {
	dirname := filepath.Join(config.RunnersDir, projectName)
	if err := os.MkdirAll(dirname, 0750); err != nil {
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

	if cwd, err := os.Getwd(); err != nil {
		return fmt.Errorf("Can't get cwd: %v", err)
	} else {
		defer os.Chdir(cwd)
	}
	if err := os.Chdir(dirname); err != nil {
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

	arcfile, err := GetRunnerArchive(optset.Entity)
	if err != nil {
		log.Fatal(err)
	}

	if err := InstallToDir(arcfile, name, ProjectUrl, ProjectToken, labels); err != nil {
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

func TryAction(args []string) {
	ReadConfig()
	optset := NewEntityOptset(args)
	optset.SetParameters("")
	optset.Parse()

	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	fmt.Printf("os: %s\n", runtime.GOOS)
	fmt.Printf("arch: %s\n", arch)

	res, err := GitHubGetDownloads(optset.Entity)
	if err != nil {
		log.Fatal(err)
	}
	for _, dn := range res {
		if dn.OS == runtime.GOOS && dn.Arch == arch {
			fmt.Println(dn)
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
		"try":    Action{Action: TryAction, Help: "Dont use it, unless you know what you're doing"},
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
