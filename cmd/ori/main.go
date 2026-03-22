package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/QuakeWang/ori/internal/ui"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	ui.ConfigureLoggingFromEnv()
	rootCmd := ui.RootCmd()
	rootCmd.AddCommand(versionCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			if version == "dev" {
				fmt.Println("ori dev")
			} else {
				fmt.Printf("ori %s (%s, %s)\n", version, commit, buildDate)
			}
		},
	}
}
