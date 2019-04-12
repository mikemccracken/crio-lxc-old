package main

import (
	"fmt"
	"os"

	"github.com/apex/log"
	"github.com/urfave/cli"
	//	"gopkg.in/yaml.v2"
)

var (
	version = ""
	debug   = false
)

func main() {
	app := cli.NewApp()
	app.Name = "crio-lxc"
	app.Usage = "crio-lxc is a CRI compliant runtime wrapper for lxc"
	app.Version = version
	app.Commands = []cli.Command{
		stateCmd,
		createCmd,
		startCmd,
		killCmd,
		deleteCmd,
	}

	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:  "debug",
			Usage: "enable debug mode",
		},
		cli.StringFlag{
			Name:  "log-level",
			Usage: "set log level for LXC",
		},
		cli.StringFlag{
			Name:  "log-file",
			Usage: "log file for LXC",
		},
	}

	app.Before = func(ctx *cli.Context) error {
		//var err error

		debug = ctx.Bool("debug")
		return nil
	}

	log.SetLevel(log.InfoLevel)

	if err := app.Run(os.Args); err != nil {
		format := "error: %v\n"
		if debug {
			format = "error: %+v\n"
		}

		fmt.Fprintf(os.Stderr, format, err)
		os.Exit(1)
	}
}
