package cmd

import (
    "fmt"
    "os"
    "path/filepath"

    "github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
    return &cobra.Command{
        Use:          "list",
        Short:        "List bitswan workspaces",
        Args:         cobra.NoArgs,
        SilenceUsage: true,
        RunE: func(cmd *cobra.Command, args []string) error {
            bitswanConfig := filepath.Join(os.Getenv("HOME"), ".config", "bitswan", "workspaces")

            // Check if directory exists
            if _, err := os.Stat(bitswanConfig); os.IsNotExist(err) {
                return fmt.Errorf("workspaces directory not found: %s", bitswanConfig)
            }

            // Read directory entries
            entries, err := os.ReadDir(bitswanConfig)
            if err != nil {
                return fmt.Errorf("failed to read workspaces directory: %w", err)
            }

            // Print each subdirectory
            for _, entry := range entries {
                if entry.IsDir() {
                    fmt.Fprintln(cmd.OutOrStdout(), entry.Name())
                }
            }

            return nil
        },
    }
}
