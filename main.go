package main

import "github.com/atyronesmith/ai-helps-jira/cmd"

var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

func main() {
	cmd.SetVersionInfo(version, commit, buildDate)
	cmd.Execute()
}
