package main

import (
	"fmt"
	"os"
	"os/user"
	"sort"
	"strings"

	"github.com/blacknon/lssh/check"
	"github.com/blacknon/lssh/common"
	"github.com/blacknon/lssh/conf"
	"github.com/blacknon/lssh/list"
	"github.com/blacknon/lssh/ssh"
	"github.com/urfave/cli"
)

func Lscp() (app *cli.App) {
	// Default config file path
	usr, _ := user.Current()
	defConf := usr.HomeDir + "/.lssh.conf"

	// Set help templete
	cli.AppHelpTemplate = `NAME:
    {{.Name}} - {{.Usage}}
USAGE:
    {{.HelpName}} {{if .VisibleFlags}}[options]{{end}} (local|remote):from_path... (local|remote):to_path
    {{if len .Authors}}
AUTHOR:
    {{range .Authors}}{{ . }}{{end}}
    {{end}}{{if .Commands}}
COMMANDS:
    {{range .Commands}}{{if not .HideHelp}}{{join .Names ", "}}{{ "\t"}}{{.Usage}}{{ "\n" }}{{end}}{{end}}{{end}}{{if .VisibleFlags}}
OPTIONS:
    {{range .VisibleFlags}}{{.}}
    {{end}}{{end}}{{if .Copyright }}
COPYRIGHT:
    {{.Copyright}}
    {{end}}{{if .Version}}
VERSION:
    {{.Version}}
    {{end}}
USAGE:
    # local to remote scp
    {{.Name}} /path/to/local... remote:/path/to/remote

    # remote to local scp
    {{.Name}} remote:/path/to/remote... /path/to/local

    # remote to remote scp
    {{.Name}} remote:/path/to/remote... remote:/path/to/local
`
	// Create app
	app = cli.NewApp()
	app.Name = "lscp"
	app.Usage = "TUI list select and parallel scp client command."
	app.Copyright = "blacknon(blacknon@orebibou.com)"
	app.Version = "0.5.6"

	app.Flags = []cli.Flag{
		cli.StringSliceFlag{Name: "host,H", Usage: "connect servernames"},
		cli.BoolFlag{Name: "list,l", Usage: "print server list from config"},
		cli.StringFlag{Name: "file,f", Value: defConf, Usage: "config file path"},
		cli.BoolFlag{Name: "permission,p", Usage: "copy file permission"},
		cli.BoolFlag{Name: "help,h", Usage: "print this help"},
	}
	app.EnableBashCompletion = true
	app.HideHelp = true

	app.Action = func(c *cli.Context) error {
		// show help messages
		if c.Bool("help") {
			cli.ShowAppHelp(c)
			os.Exit(0)
		}

		hosts := c.StringSlice("host")
		confpath := c.String("file")

		// check count args
		if len(c.Args()) < 2 {
			fmt.Fprintln(os.Stderr, "Too few arguments.")
			cli.ShowAppHelp(c)
			os.Exit(1)
		}

		// Set args path
		fromsArgs := c.Args()[:c.NArg()-1]
		toArg := c.Args()[c.NArg()-1]

		isFromInRemote := false
		isFromInLocal := false
		for _, from := range fromsArgs {
			// parse args
			isFromRemote, _ := check.ParseScpPath(from)

			if isFromRemote {
				isFromInRemote = true
			} else {
				isFromInLocal = true
			}
		}
		isToRemote, _ := check.ParseScpPath(toArg)

		// Check from and to Type
		check.CheckTypeError(isFromInRemote, isFromInLocal, isToRemote, len(hosts))

		// Get config data
		data := conf.ReadConf(confpath)

		// Get Server Name List (and sort List)
		names := conf.GetNameList(data)
		sort.Strings(names)

		selected := []string{}
		toServer := []string{}
		fromServer := []string{}

		// view server list
		switch {
		// connectHost is set
		case len(hosts) != 0:
			if check.ExistServer(hosts, names) == false {
				fmt.Fprintln(os.Stderr, "Input Server not found from list.")
				os.Exit(1)
			} else {
				toServer = hosts
			}

		// remote to remote scp
		case isFromInRemote && isToRemote:
			// View From list
			from_l := new(list.ListInfo)
			from_l.Prompt = "lscp(from)>>"
			from_l.NameList = names
			from_l.DataList = data
			from_l.MultiFlag = false
			from_l.View()
			fromServer = from_l.SelectName
			if fromServer[0] == "ServerName" {
				fmt.Fprintln(os.Stderr, "Server not selected.")
				os.Exit(1)
			}

			// View to list
			to_l := new(list.ListInfo)
			to_l.Prompt = "lscp(to)>>"
			to_l.NameList = names
			to_l.DataList = data
			to_l.MultiFlag = true
			to_l.View()
			toServer = to_l.SelectName
			if toServer[0] == "ServerName" {
				fmt.Fprintln(os.Stderr, "Server not selected.")
				os.Exit(1)
			}

		default:
			// View List And Get Select Line
			l := new(list.ListInfo)
			l.Prompt = "lscp>>"
			l.NameList = names
			l.DataList = data
			l.MultiFlag = true
			l.View()

			selected = l.SelectName
			if selected[0] == "ServerName" {
				fmt.Fprintln(os.Stderr, "Server not selected.")
				os.Exit(1)
			}

			if isFromInRemote {
				fromServer = selected
			} else {
				toServer = selected
			}
		}

		// scp struct
		runScp := new(ssh.RunScp)

		// set from info
		for _, from := range fromsArgs {
			// parse args
			isFromRemote, fromPath := check.ParseScpPath(from)

			// Check local file exisits
			if !isFromRemote {
				_, err := os.Stat(common.GetFullPath(fromPath))
				if err != nil {
					fmt.Fprintf(os.Stderr, "not found path %s \n", fromPath)
					os.Exit(1)
				}
				fromPath = common.GetFullPath(fromPath)
			}

			// set from data
			runScp.From.IsRemote = isFromRemote
			if isFromRemote {
				fromPath = check.EscapePath(fromPath)
			}
			runScp.From.Path = append(runScp.From.Path, fromPath)

		}
		runScp.From.Server = fromServer

		// set to info
		isToRemote, toPath := check.ParseScpPath(toArg)
		runScp.To.IsRemote = isToRemote
		if isToRemote {
			toPath = check.EscapePath(toPath)
		}
		runScp.To.Path = []string{toPath}
		runScp.To.Server = toServer

		runScp.Permission = c.Bool("permission")
		runScp.Config = data

		// print from
		if !isFromInRemote {
			fmt.Fprintf(os.Stderr, "From local:%s\n", runScp.From.Path)
		} else {
			fmt.Fprintf(os.Stderr, "From remote(%s):%s\n", strings.Join(runScp.From.Server, ","), runScp.From.Path)
		}

		// print to
		if !isToRemote {
			fmt.Fprintf(os.Stderr, "To   local:%s\n", runScp.To.Path)
		} else {
			fmt.Fprintf(os.Stderr, "To   remote(%s):%s\n", strings.Join(runScp.To.Server, ","), runScp.To.Path)
		}

		runScp.Start()
		return nil
	}

	return app
}
