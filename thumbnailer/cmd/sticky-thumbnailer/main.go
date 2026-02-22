package main

import (
	overseer "github.com/whisper-darkly/sticky-overseer/v2"
	_ "github.com/whisper-darkly/sticky-thumbnailer/handler" // registers "thumbnailer" factory
)

var version = "dev"
var commit = "unknown"

func main() { overseer.RunCLI(version, commit) }
