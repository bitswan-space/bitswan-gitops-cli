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
	var long bool

	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List available bitswan workspaces",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			workspacesDir := filepath.Join(os.Getenv("HOME"), ".config", "bitswan", "workspaces")

			// Check if directory exists
			if _, err := os.Stat(workspacesDir); os.IsNotExist(err) {
				return fmt.Errorf("workspaces directory not found: %s", workspacesDir)
			}

			// Read directory entries
			entries, err := os.ReadDir(workspacesDir)
			if err != nil {
				return fmt.Errorf("failed to read workspaces directory: %w", err)
			}

			// Print each subdirectory
			for _, entry := range entries {
				if entry.IsDir() {
					workspaceName := entry.Name()
					fmt.Fprintln(cmd.OutOrStdout(), workspaceName)
					if long {
						domain, editorURL, gitopsURL := getMetaData(workspaceName, workspacesDir)
						if domain != "" {
							fmt.Fprintf(cmd.OutOrStdout(), "  Workspace Domain: %s\n", domain)
						}
						if editorURL != "" {
							fmt.Fprintf(cmd.OutOrStdout(), "  Editor URL: %s\n", editorURL)
						}
						if gitopsURL != "" {
							fmt.Fprintf(cmd.OutOrStdout(), "  Gitops URL: %s\n", gitopsURL)
						}
					}

					if showPasswords {
						// Get VSCode server password
						vscodePassword, _ := getVSCodePassword(workspaceName)
						if vscodePassword != "" {
							fmt.Fprintf(cmd.OutOrStdout(), "  VSCode Password: %s\n", vscodePassword)
						}

						// Get GitOps secret
						gitopsSecret, _ := getGitOpsSecret(workspaceName, workspacesDir)
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
	cmd.Flags().BoolVarP(&long, "long", "l", false, "Show verbose output")

	return cmd
}

func getMetaData(workspaceName string, workspacesDir string) (string, string, string) {
	// Path to metadata.yaml file
	metadataPath := filepath.Join(workspacesDir, workspaceName, "metadata.yaml")

	// Check if metadata file exists
	if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
		return "", "", ""
	}

	// Read metadata file
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return "", "", ""
	}

	// Parse YAML
	var metadata struct {
		Domain    string `yaml:"domain"`
		EditorURL string `yaml:"editor-url"`
		GitopsURL string `yaml:"gitops-url"`
	}

	if err := yaml.Unmarshal(data, &metadata); err != nil {
		return "", "", ""
	}

	return metadata.Domain, metadata.EditorURL, metadata.GitopsURL
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

func getGitOpsSecret(workspace string, workspacesDir string) (string, error) {
	// Read docker-compose.yml file
	composeFilePath := filepath.Join(workspacesDir, workspace, "deployment", "docker-compose.yml")

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
