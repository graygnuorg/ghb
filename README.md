# GHB - A manager for GitHub self-hosted runners

__Ghb__ manages a farm of [GitHub self-hosted runners](https://docs.github.com/en/actions/hosting-your-own-runners)
for one or more projects.  It uses [GNU pies](http://pies.software.gnu.org.ua/) to execute runners, collect
their output and keep logs.  You will need GNU pies version 1.7.92 or newer.

## General usage

The command gets the _action verb_ as its argument.  It can be followed by command line options and arguments
specific for that particular action.  To obtain a short command line usage summary, run

```sh
ghb help
```

to obtain a command line usage for a particular action, run

```sh
ghb action --help
```

The sections below will lead you through the most often used __ghb__ actions, thus providing the necessary
information for a quick start.  For a detailed discussion of each available action, refer to the
[action summary](#user-content-Actions) section

## Setup

To setup __ghb__ run:

```sh
ghb setup
```

This will create the necessary directory structure and start __pies__.  By default, all files
will be located in __~/GHB__.  The created pies configuration file uses port 8073 on localhost to communicate
with the running __pies__ instance.  If that port is already in use, supply another one using the
`--port` command line option.

Normally __ghb__ doesn't need a configuration file, as it has sane built-in default configuration.  These
defaults are what the __setup__ command uses.  Nevertheless, if need be, you can create a configuration file
with your customized settings.  To create a default configuration file during the setup process, use the
`--make-config` option.  Refer to [configuration](#user-content-Configuration) section for further
instructions.

## Authentication entities

Many __ghb__ actions need access credentials (_tokens_) to send their requests to GitHub.  These credentials
depend on the _authentication entity_ (hereinafter referred to as _entity_).  There are three kinds of
entities:

* Repository

  This basic authentication entity represents a GitHub repository.  It is supplied to the action using the
  `--repo` command line option.  Its argument is the name of the user, organization or enterprise that owns
  the repository, followed by a slash, followed by the name of the repository.  For example:

  ```
  ghb action --repo foo/bar
  ```

  When operating on a particular GitHub user, the slash and repository part may be omitted.

* Organization

  When operating on a GitHub _organization_, its name is supplied with the `--org` command line option.

* Enterprise

  When operating on a GitHub _enterprise_, its name is supplied with the `--enterprise` command line option,

The `--repo`, `--org` and `--enterprise` options are mutually exclusive.

## Tokens

The program obtains tokens necessary for creation or deletion of the runners as needed.  To do so, it uses the
corresponding GitHub [private access token](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/creating-a-personal-access-token)
(_PAT_ for short).

Before proceeding with adding new self-hosted runners, you need to create the PAT.  Depending on the
authentication entity, this PAT must have different _scopes_:

* Repository

  `repo`

* Enterprise

  `manage_runners:enterprise`

* Organization

  `admin:org`
  
Once you have obtained a PAT, use the `ghb pat` command to store it for the further use:


* Repository

  ```sh
  ghb pat --repo NAME --set TOKEN --expire EXP
  ```

* Enterprise

  ```sh
  ghb pat --enterprize NAME --set TOKEN --expire EXP
  ```

* Organization

  ```sh
  ghb pat --org NAME --set TOKEN --expire EXP
  ```

Here, the _NAME_ argument is the name of the corresponding entity, _TOKEN_ is the private access token you
obtained from GitHub and _EXP_ is its expiration date or duration.  To specify the date, use the following
format: `2006-01-02 15:04:05`.  To specify duration, precede the value with a plus sign and use the unit
suffixes "s", "m", "h", for seconds, minutes and hours, correspondingly (e.g. --exp 720h20m),

To list the PAT for a given organization, use the `ghb pat` action as follows:

```sh
ghb pat --org NAME
```

Replace `--org` with another entity option, as necessary.

To list both the PAT and any other keys obtained using it during the normal operation, use the `--all` (`-a`)
command line option:

```sh
ghb pat --org NAME --all
```

## Listing runners and current status

To list the configured self-hosted runners, use the `list` action:

```sh
ghb list
```

The output shows on each line, the name of the directory where runner subdirectories are stored (relative to
the [runners_dir](#user-content-Configuration)), number of runners configured and running and the ID (ordinal
number) of a new runner that will be added by the `ghb add` command, e.g.:

```
/orgs/ExampleName                   2 2
/repos/foo/bar                      1 1
```

The first directory name component can be used to discern between various entities: it is `/repos` for the
repositories, `/orgs` for organizations and `/enterprises`, for enterprises.

The `ghb status` command can be used to display the current program status:

```sh
$ ghb status
Using built-in configuration defaults
Configuration file passed syntax check
GNU Pies 1.7.92 running with PID 23452
7 runners active
```

## Adding runners

Use the `add` command to configure new self-hosted runners.  For example, to add a new global runner to the
organisation `ExampleOrg`, do:

```sh
ghb add --org ExampleOrg
```

Similarly, to add a runner to the repository `ExampleOrg/foo`, do:

```sh
ghb add --repo ExampleOrg/foo
```

The command will determine the OS version and architecture it is running on, download the necessary runner
archive (or use a previously downloaded copy, if available), extract it, configure the new runner, incorporate
it in the GNU pies configuration and start the new runner.

Notice, that the latter example can also be written as

```sh
ghb add --repo ExampleOrg foo
```

## Deleting a runner

To delete a runner, use the `delete` action:

```sh
ghb delete --repo ExampleOrg/foo
```

This command deletes the most recently added runner.  You can delete any particular runner by specifying its
ordinal number (0-based) with the `--id` option, e.g.:

```sh
ghb delete --repo ExampleOrg/foo --id 2
```

## Starting and stopping

The actions `start`, `stop` and `restart` manage the running instance of GNU pies.  The command

```sh
ghb stop
```

stops all configured runners and shuts down the running __pies__ instance.  The command

```sh
ghb start
```

does the reverse: starts up __pies__, which in its turn will bring up all configured runners.

The command

```sh
ghb restart
```

does both actions in sequence.

## Configuration

The program looks for its configuration file `ghb.conf` in the user home directory.  It is not an error, if it
does not exist - in this case the program uses its built-in defaults.  In fact, it is the normal way of
operation.  Configuration file may become necessary if you need to change the location of `ghb` files or
change the template for runner components in `pies.conf`.

The configuration is kept in YAML format.  The following attributes are defined:

* `root_dir`

Name of the top-level `gha` directory.  All other files and directories are located underneath this
directory, unless they are explicitly configured with an absolute file name.

* `runners_dir`

Directory for storing working directories of the runners.  At most three subdirectories will be created in
this directory: `enterprises/`, for global enterprise runners, `orgs/` for global organization runners, and
`repos/` for individual repository runners.  The default value for this parameter is `runners`.

* `cache_dir`

Name of the cache directory.  Defaults to `cache`.

* `tar`

File name of the `tar` utility.  Defaults to `tar`.  Use this if `tar` is located outside system `PATH` or
has been renamed.

* `pies`

File name of the GNU `pies` utility.  Defaults to `pies` (i.e. it is looked up in system `PATH`).

* `pies_config_file`

Name of the `pies` configuration file.  The default value is `pies.conf`.

* `component_template`

[Golang template](https://pkg.go.dev/text/template) for adding new runners to the pies configuration file.
After expansion, it must produce a valid pies [component statement](https://puszcza.gnu.org.ua/software/pies/manual/Component-Statement.html).

The default value is:

```conf
component "{{ RunnerName }}" {
        mode respawn;
        chdir "{{ Config.RunnersDir }}/{{ RunnerName }}";
        stderr syslog daemon.err;
        stdout syslog daemon.info;
        flags siggroup;
        command "./run.sh";
}
```

The following `ghb`-specific functions are available for use in this template:

* `RunnerName`

  Name of the runner working directory under `runners_dir`.

* `Config`

  Current `ghb` configuration.  Configuration parameters are addressed by converting their names to camel
  case, e.g. `runners_dir` becomes `Config.RunnersDir` and so on.

## Actions

### `add` - Add a runner

```sh
ghb add --org|--enterprise|--repo ENTITY [OPTIONS] [NAME]
```

Optional _NAME_ can be used with the `--repo` option.  In this case, _ENTITY_ shoulb be the name of the
corresponding entity, without the `/REPO` part.  That is, the following two invocations are equivalent:

```sh
ghb add --repo example/proj
```

and

```sh
ghb add --repo example proj
```

Options:

* `-g`, `--runnergroup=`_STRING_

  Name of the runner group
  
* `-l`, `--labels=`_STRING_

  Extra labels to associate with the runner, in addition to the default
  
* `-t`, `--token=`_STRING_

  Project registration token to use instead of the automatically retrieved one.
  
* `-u`, `--url=`_URL_     Project URL

  Project URL.  It is normally determined automatically.

* `-h`, `--help`

  Display a short help summary and exit.

### `configcheck` - Check current configuration

This command verifies the current configuration.  Each configuration setting is printed on a separate line,
along with its current value and status (`OK` or a diagnostic message describing the problem).  The program
terminates with code 0 if the configuration is OK, or 1 if it is not.  E.g.:

```sh
$ ghb configcheck
Verifying configuration
  root_dir = "/home/gray/GHB": OK
  runners_dir = "/home/gray/GHB/runners": OK
  cache_dir = "/home/gray/GHB/cache": OK
  tar = "tar": OK
  pies = "pies": OK
  pies_config_file = "/home/gray/GHB/pies.conf": OK
  component_template = "component \"{{...": OK
```

Options:

* `-l`, `--list`

  Show the configuration in form of annotated YAML on the output.

* `-h`, `--help`

  Display a short help summary and exit.

### `delete` - Delete a runner

```sh
gh delete --org|--enterprise|--repo ENTITY [OPTIONS]
```

This command deletes the last registered runner.  The `--id` option can be used to request deletion of any
runner by its ordinal number.  By default, the runner is properly deregistered from GitHub and its working
directory is removed.  This behavior can be changed using the `--keep` and `--force` options.

Options:

* `-f`, `--force`

  Force removal of the runner and its directory, even if unable to deregister it from GitHub.
  
* `-i`, `--id=`_NUMBER_

  Runner ID (0-based ordinal number).  By default, the last runner is removed.
  
* `-k`, `--keep`

  Keep the configured runner directory, do not deregister it from GitHUb.
  
* `--token=`_STRING_

  Removal token to use instead of the automatically retrieved one.
  
* `-h`, `--help`

  Display a short help summary and exit.

### `help` - Show a short help summary

```sh
gh help
```

Displays a short command line usage summary and a list of available actions.

### `list` - List existing runners

```sh
gh list [OPTIONS]
```

This command lists configured runners:

```sh
$ ghb list
/orgs/ExampleOrg                    3 3
/repos/foo/project                  5 7
```

Each output line lists the pathname of the runner working directory, relative to
[runners_dir](#user-content-Configuration), total number of configured runners and the number to be assigned to
the next runner by the `ghb add` command.  When given the `--verbose` option, additional lines are printed
after each summary line, describing each configured runner in detail.  These lines include runner number, its
working directory and locations in the `pies.conf` file where it is configured.

Options:

* `-v`, `--verbose`

  Verbosely list each runner location

* `-h`, `--help`

  Display a short help summary and exit.

### `pat` - Manage private access keys

```sh
ghb pat --org|--enterprise|--repo ENTITY [OPTIONS]
```

This command manages private access tokens.  Unless other options are specified, it lists private access
token for the given entity.  When given the `--all` option, it lists additionally any registration and
deletion keys obtained using this PAT.

The `--set` option stores the new value of the token.  Normally, it is used with the `--expires` option,
which sets the expiration time.  The argument to the `--expires` option is either the absolute expiration time
in format _YYYY-MM-DD HH:MM:SS_, or the [duration period](https://pkg.go.dev/time#ParseDuration).  The default
expiration time is one month from the current date.

Options:

* `-a`, `--all`

  List all keys for the given entity.
  
* `-d`, `--delete`

  Delete PAT.

* `-e`, `--expires=`_TIME_

  Set expire time or duration (with the --set option).

* `-s`, `--set=`_STRING_

  Set new PAT.

* `-h`, `--help`

  Display a short help summary and exit.

### `restart` - Restart GNU pies supervisor

```sh
ghb restart
```

This command restarts the GNU `pies` supervisor.  If `pies` is already running, it is equivalent to

```sh
ghb stop
ghb start
```

Otherwise, it is equivalent to `ghb start`.

### `setup` - Set up GHB subsystem

```sh
ghb setup [OPTIONS]
```

Sets up the `ghb` directory structure, creates the necessary files and starts up `pies`.  If the system is
already set up, the command reports the fact and terminates.

Options:

* `--make-config`

  Create `ghb.conf` configuration file in the user home directory.  Normally, `ghb` does not need an explicit
  [configuration file]](#user-content-Configuration).  If you intend to tweak with the configuration, you can
  create one by using this option.

* `--port=`_PORT_

  Change `pies` control port.  Use this option if the default port 8073 is already in use on your system,

* `-h`, `--help`

  Display a short help summary and exit.

### `start` - Start GNU pies supervisor

```sh
ghb start
```

Starts GNU `pies`.

### `status` - Check ghb system status

```sh
ghb status [OPTIONS]
```

This command checks the configuration settings and displays information about the `ghb` status:

```
$ ghb status
Using built-in configuration defaults
Configuration file passed syntax check
GNU Pies 1.7.92 running with PID 3694
4 runners active
```

Options:

* `-v`, `--verbose`

  Display verbose configuration status information.

* `-h`, `--help`

  Display a short help summary and exit.

### `stop` - Stop GNU pies supervisor

```
ghb stop
```

Stops the `pies` supervisor.
