package main

import (
	"fmt"
	"golang.org/x/sys/unix"

	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/apex/log"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
	"github.com/urfave/cli"

	lxc "gopkg.in/lxc/go-lxc.v2"
)

var syncFifoWaitHook = []byte(`
#!/bin/bash
log=/var/lib/lxc/$LXC_NAME/syncfifo-hook-log-$(date --iso-8601=s)

fifo=/var/lib/lxc/$LXC_NAME/syncfifo
[ -p $fifo ] || {
    echo "error: $fifo not found or not a fifo"
    exit 1
}
echo "started, waiting on $fifo" >$log
echo "ready">$fifo
echo "done, $fifo was read from">>$log
`)

var createCmd = cli.Command{
	Name:      "create",
	Usage:     "create a container from a bundle directory",
	ArgsUsage: "<containerID>",
	Action:    doCreate,
	Flags: []cli.Flag{
		cli.StringFlag{
			Name:  "bundle",
			Usage: "set bundle directory",
			Value: ".",
		},
		cli.IntFlag{
			Name:  "console-socket",
			Usage: "pty master FD", // TODO not handled yet
		},
		cli.StringFlag{
			Name:  "pid-file",
			Usage: "path to write container PID", // TODO not handled yet
		},
	},
}

func doCreate(ctx *cli.Context) error {
	containerID := ctx.Args().Get(0)
	if len(containerID) == 0 {
		fmt.Fprintf(os.Stderr, "missing container ID\n")
		cli.ShowCommandHelpAndExit(ctx, "create", 1)
	}
	log.Infof("creating container %s", containerID)

	exists, err := containerExists(containerID)
	if err != nil {
		return errors.Wrap(err, "failed to check if container exists")
	}
	if exists {
		return fmt.Errorf("container '%s' already exists", containerID)
	}

	c, err := lxc.NewContainer(containerID, LXC_PATH)
	if err != nil {
		return errors.Wrap(err, "failed to create new container")
	}
	defer c.Release()

	spec, err := readBundleSpec(filepath.Join(ctx.String("bundle"), "config.json"))
	if err != nil {
		return errors.Wrap(err, "couldn't load bundle spec")
	}

	if err := os.MkdirAll(filepath.Join(LXC_PATH, containerID), 0770); err != nil {
		return errors.Wrap(err, "failed to create container dir")
	}

	syncFifoWaitHookFilename := filepath.Join(LXC_PATH, containerID, "sync-fifo-wait")
	if err := ioutil.WriteFile(syncFifoWaitHookFilename, syncFifoWaitHook, 0777); err != nil {
		return errors.Wrap(err, "failed to write sync hook")
	}

	// add start-host hook to block on the syncfifo until `crio-lxc start`
	if err := c.SetConfigItem("lxc.hook.start-host", syncFifoWaitHookFilename); err != nil { // TODO fix to start-host
		return errors.Wrap(err, "failed to set lxc.hook.start-host")
	}

	if err := makeSyncFifo(filepath.Join(LXC_PATH, containerID)); err != nil {
		return errors.Wrap(err, "failed to make sync fifo")
	}

	if err := configureContainer(ctx, c, spec); err != nil {
		return errors.Wrap(err, "failed to configure container")
	}

	log.Infof("created syncfifo, executing %#v", spec.Process.Args)

	if err := startContainer(c, spec); err != nil {
		return errors.Wrap(err, "failed to start the container init")
	}

	log.Infof("created container %s in lxcdir %s", containerID, LXC_PATH)
	return nil
}

func configureContainer(ctx *cli.Context, c *lxc.Container, spec *specs.Spec) error {
	if ctx.Bool("debug") {
		c.SetVerbosity(lxc.Verbose)
	}

	if err := configureLogging(ctx, c); err != nil {
		return errors.Wrap(err, "failed to configure logging")
	}

	// rootfs
	// todo Root.Readonly? - use lxc.rootfs.options
	if err := c.SetConfigItem("lxc.rootfs.path", spec.Root.Path); err != nil {
		return errors.Wrapf(err, "failed to set rootfs: '%s'", spec.Root.Path)
	}
	if err := c.SetConfigItem("lxc.rootfs.managed", "0"); err != nil {
		return errors.Wrap(err, "failed to set rootfs.managed to 0")
	}

	for _, envVar := range spec.Process.Env {
		if err := c.SetConfigItem("lxc.environment", envVar); err != nil {
			return fmt.Errorf("error setting environment variable '%s': %v", envVar, err)
		}
	}

	for _, ms := range spec.Mounts {
		opts := strings.Join(ms.Options, ",")
		mnt := fmt.Sprintf("%s %s %s %s", ms.Source, ms.Destination, ms.Type, opts)
		if err := c.SetConfigItem("lxc.mount.entry", mnt); err != nil {
			return errors.Wrap(err, "failed to set mount config")
		}
	}

	if err := c.SetConfigItem("lxc.init.cwd", spec.Process.Cwd); err != nil {
		return errors.Wrap(err, "failed to set CWD")
	}

	if err := c.SetConfigItem("lxc.uts.name", spec.Hostname); err != nil {
		return errors.Wrap(err, "failed to set hostname")
	}

	argsString := strings.Join(spec.Process.Args, " ")
	if err := c.SetConfigItem("lxc.execute.cmd", argsString); err != nil {
		return errors.Wrap(err, "failed to set lxc.execute.cmd")

	}
	if err := c.SetConfigItem("lxc.hook.version", "1"); err != nil {
		return errors.Wrap(err, "failed to set hook version")
	}

	// capabilities?

	// if !spec.Process.Terminal {
	// 	passFdsToContainer()
	// }

	// Write out final config file for debugging and use with lxc-attach:
	// Do not edit config after this.
	savedConfigFile := filepath.Join(lxc.DefaultConfigPath(), c.Name(), "config")
	if err := c.SaveConfigFile(savedConfigFile); err != nil {
		return errors.Wrapf(err, "failed to save config file to '%s'", savedConfigFile)
	}

	return nil
}

func makeSyncFifo(dir string) error {
	fifoFilename := filepath.Join(dir, "syncfifo")
	prevMask := unix.Umask(0000)
	defer unix.Umask(prevMask)
	if err := unix.Mkfifo(fifoFilename, 0622); err != nil {
		return errors.Wrapf(err, "failed to make fifo '%s'", fifoFilename)
	}
	return nil
}

func startContainer(c *lxc.Container, spec *specs.Spec) error {
	binary, err := os.Readlink("/proc/self/exe")
	if err != nil {
		return err
	}

	cmd := exec.Command(
		binary,
		"internal",
		c.Name(),
		spec.Root.Path,
		filepath.Join("/var/lib/lxc", c.Name(), "config"),
	)

	if !spec.Process.Terminal {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	cmdErr := cmd.Run()

	return cmdErr

}
