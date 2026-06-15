// Package cli builds the cobra command tree for the to-do demo backend and
// executes it via charm.land/fang/v2, mirroring the OVDB server's CLI stack.
package cli

import (
	"context"

	"charm.land/fang/v2"
	"github.com/spf13/cobra"

	"github.com/openvaultdb/openvaultdb-todo-demo/internal/server"
)

// Run builds the root command and executes it with the given args.
func Run(args []string) error {
	root := newRootCommand()
	if len(args) > 1 {
		root.SetArgs(args[1:])
	}
	return fang.Execute(context.Background(), root, fang.WithoutVersion())
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "todo-backend",
		Short:         "OpenVaultDB to-do demo backend",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.AddCommand(serveCommand())
	return root
}

func serveCommand() *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the demo REST API and OVDB connect-flow proxy",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return server.New().Listen(cmd.Context(), port)
		},
	}
	cmd.Flags().IntVar(&port, "port", server.DefaultPort, "port to listen on")
	return cmd
}
