/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"syscall"

	"mozilla.org/simplepush"
)

var (
	configFile *string = flag.String("config", "config.ini", "Configuration File")
	profile    *string = flag.String("profile", "", "Profile file output")
	memProfile *string = flag.String("memProfile", "", "Profile file output")
	logging    *int    = flag.Int("logging", 0,
		"logging level (0=none,1=critical ... 10=verbose")
)

const SIGUSR1 = syscall.SIGUSR1
const VERSION = "1.2"

// -- main
func main() {
	flag.Parse()

	runtime.GOMAXPROCS(runtime.NumCPU())
	// Only create profiles if requested. To view the application profiles,
	// see http://blog.golang.org/profiling-go-programs
	if *profile != "" {
		log.Printf("Creating profile...")
		f, err := os.Create(*profile)
		if err != nil {
			log.Fatal(err)
		}
		defer func() {
			log.Printf("Closing profile...")
			pprof.StopCPUProfile()
		}()
		pprof.StartCPUProfile(f)
	}
	if *memProfile != "" {
		defer func() {
			profFile, err := os.Create(*memProfile)
			if err != nil {
				log.Fatalln(err)
			}
			pprof.WriteHeapProfile(profFile)
			profFile.Close()
		}()
	}

	// Load the app from the config file
	app, err := simplepush.LoadApplicationFromFileName(*configFile)
	if err != nil {
		log.Fatal(err.Error())
	}

	// Report what the app believes the current host to be, and what version.
	log.Printf("CurrentHost: %s, Version: %s", app.Hostname(), VERSION)

	// wait for sigint
	sigChan := make(chan os.Signal)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGHUP, SIGUSR1)

	// And we're underway!
	errChan := app.Run()

	select {
	case err := <-errChan:
		if err != nil {
			panic("ListenAndServe: " + err.Error())
		}
	case <-sigChan:
		app.Logger().Info("main", "Recieved signal, shutting down.", nil)
		app.Stop()
	}
}

// 04fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
