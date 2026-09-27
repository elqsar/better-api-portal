package main

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is set by release builds (task release):
// -ldflags "-X main.version=v0.1.0".
var version string

// portalVersion is the release's version, else the module version that go
// install recorded, else "devel" for a build from a checkout.
func portalVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "devel"
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the portal's version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			line := fmt.Sprintf("portal %s (%s, %s/%s", portalVersion(), runtime.Version(), runtime.GOOS, runtime.GOARCH)
			if bi, ok := debug.ReadBuildInfo(); ok {
				for _, s := range bi.Settings {
					if s.Key == "vcs.revision" && len(s.Value) >= 12 {
						line += ", commit " + s.Value[:12]
					}
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), line+")")
			return nil
		},
	}
}
