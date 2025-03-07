package cmd

import (
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "regexp"
    "strings"

    "github.com/spf13/cobra"
    "gopkg.in/yaml.v3"
)

func newListCmd() *cobra.Command {
    var showPasswords bool

    cmd := &cobra.Command{
        Use:          "list",
        Short:        "List available bitswan workspaces",
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
                    workspaceName := entry.Name()
                    fmt.Fprintln(cmd.OutOrStdout(), workspaceName)

                    if showPasswords {
                        // Get VSCode server password
                        vscodePassword, _ := getVSCodePassword(workspaceName)
                        if vscodePassword != "" {
                            fmt.Fprintf(cmd.OutOrStdout(), "  VSCode Password: %s\n", vscodePassword)
                        }

                        // Get GitOps secret
                        gitopsSecret, _ := getGitOpsSecret(workspaceName, bitswanConfig)
                        if gitopsSecret != "" {
                            fmt.Fprintf(cmd.OutOrStdout(), "  GitOps Secret: %s\n", gitopsSecret)
                        }
                    }
                }
            }

            return nil
        },
    }

    cmd.Flags().BoolVar(&showPasswords, "passwords", false, "Show VSCode server passwords and GitOps secrets")

    return cmd
}

func getVSCodePassword(workspace string) (string, error) {
    // Check if the service exists
    checkCmd := exec.Command("docker", "compose", "-p", workspace+"-site", "ps", "bitswan-editor-"+workspace)
    if err := checkCmd.Run(); err != nil {
        return "", fmt.Errorf("service not running")
    }

    // Execute docker compose command to get config.yaml content
    cmd := exec.Command("docker", "compose", "-p", workspace+"-site", "exec", "-T", "bitswan-editor-"+workspace, "cat", "/home/coder/.config/code-server/config.yaml")
    output, err := cmd.CombinedOutput()
    if err != nil {
        return "", err
    }

    // Parse the password from the output
    re := regexp.MustCompile(`password: (.+)`)
    matches := re.FindStringSubmatch(string(output))
    if len(matches) > 1 {
        return matches[1], nil
    }

    return "", fmt.Errorf("password not found in config")
}

func getGitOpsSecret(workspace string, configDir string) (string, error) {
    // Read docker-compose.yml file
    composeFilePath := filepath.Join(configDir, workspace, "deployment", "docker-compose.yml")

    data, err := os.ReadFile(composeFilePath)
    if err != nil {
        return "", err
    }

    // Parse YAML to extract the secret
    var composeConfig map[string]interface{}
    if err := yaml.Unmarshal(data, &composeConfig); err != nil {
        return "", err
    }

    // Navigate through the YAML structure to find the secret
    services, ok := composeConfig["services"].(map[string]interface{})
    if !ok {
        return "", fmt.Errorf("services section not found")
    }

    editorService, ok := services["bitswan-editor-"+workspace].(map[string]interface{})
    if !ok {
        return "", fmt.Errorf("editor service not found")
    }

    env, ok := editorService["environment"].([]interface{})
    if !ok {
        return "", fmt.Errorf("environment section not found")
    }

    // Look for the BITSWAN_DEPLOY_SECRET in the environment variables
    for _, item := range env {
        envVar, ok := item.(string)
        if !ok {
            continue
        }

        if strings.HasPrefix(envVar, "BITSWAN_DEPLOY_SECRET=") {
            parts := strings.SplitN(envVar, "=", 2)
            if len(parts) == 2 {
                return parts[1], nil
            }
        }
    }

    return "", fmt.Errorf("GitOps secret not found")
}
