package app

import "os"

func getArgs() (args []string, version bool, update bool) {
	for _, arg := range os.Args[1:] {
		if arg == "-v" || arg == "--version" {
			version = true
			continue
		}
		if arg == "-u" || arg == "-U" || arg == "--update" {
			update = true
			continue
		}
		args = append(args, arg)
	}
	return args, version, update
}
