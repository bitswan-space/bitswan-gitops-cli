package dockercompose

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/dchest/uniuri"
	"gopkg.in/yaml.v3"
)

type OS int

const (
	WindowsMac OS = iota
	Linux
)

func CreateDockerComposeFile(gitopsPath, gitopsName, gitopsImage, bitswanEditorImage, domain string, noIde bool) (string, string, error) {
	sshDir := os.Getenv("HOME") + "/.ssh"
	gitConfig := os.Getenv("HOME") + "/.gitconfig"

	hostOsTmp := runtime.GOOS

	var hostOs OS
	switch hostOsTmp {
	case "windows", "darwin":
		hostOs = WindowsMac
	case "linux":
		hostOs = Linux
	default:
		return "", "", fmt.Errorf("unsupported host OS: %s", hostOsTmp)
	}

	// generate a random secret token
	gitopsSecretToken := uniuri.NewLen(64)

	gitopsService := map[string]interface{}{
		"image":    gitopsImage,
		"restart":  "always",
		"networks": []string{"bitswan_network"},
		"volumes": []string{
			gitopsPath + "/gitops:/gitops/gitops",
			gitopsPath + "/secrets:/gitops/secrets",
			sshDir + ":/root/.ssh",
			"/var/run/docker.sock:/var/run/docker.sock",
		},
		"environment": []string{
			"BITSWAN_GITOPS_DIR=/gitops",
			"BITSWAN_GITOPS_DIR_HOST=" + gitopsPath,
			"BITSWAN_GITOPS_ID=" + gitopsName,
			"BITSWAN_GITOPS_SECRET=" + gitopsSecretToken,
			"BITSWAN_GITOPS_DOMAIN=" + domain,
		},
	}

	if hostOs == WindowsMac {
		gitopsVolumes := []string{
			gitConfig + ":/root/.gitconfig",
			gitopsPath + "/workspace/.git:/workspace-repo/.git",
		}

		gitopsService["volumes"] = append(gitopsService["volumes"].([]string), gitopsVolumes...)

		// Rewrite .git in worktree because it's calling git command inside the container (only for Windows and Mac)
		gitdir := "gitdir: /workspace-repo/.git/worktrees/gitops"
		if err := os.WriteFile(gitopsPath+"/gitops/.git", []byte(gitdir), 0644); err != nil {
			return "", "", fmt.Errorf("failed to rewrite gitops worktree .git file: %w", err)
		}
	} else if hostOs == Linux {
		gitopsService["privileged"] = true
		gitopsService["pid"] = "host"

		gitopsEnvVars := []string{
			"HOST_PATH=$PATH",
			"HOST_HOME=$HOME",
			"HOST_USER=$USER",
		}
		gitopsService["environment"] = append(gitopsService["environment"].([]string), gitopsEnvVars...)
	}

	// Construct the docker-compose data structure
	dockerCompose := map[string]interface{}{
		"version": "3.8",
		"services": map[string]interface{}{
			gitopsName: gitopsService,
		},
		"networks": map[string]interface{}{
			"bitswan_network": map[string]interface{}{
				"external": true,
			},
		},
	}

	if !noIde {
		bitswanEditor := map[string]interface{}{
			"image":    bitswanEditorImage,
			"restart":  "always",
			"networks": []string{"bitswan_network"},
			"environment": []string{
				"BITSWAN_DEPLOY_URL=" + fmt.Sprintf("http://%s", net.JoinHostPort(gitopsName, "8079")),
				"BITSWAN_DEPLOY_SECRET=" + gitopsSecretToken,
				"BITSWAN_GITOPS_DIR=/home/coder/workspace",
			},
			"volumes": []string{
				gitopsPath + "/workspace:/home/coder/workspace/workspace",
				gitopsPath + "/secrets:/home/coder/workspace/secrets",
				gitopsPath + "/codeserver-config:/home/coder/.config/code-server/",
				sshDir + ":/home/coder/.ssh",
				"bitswan-editor-data:/home/coder",
			},
		}

		dockerCompose["services"].(map[string]interface{})[fmt.Sprintf("bitswan-editor-%s", gitopsName)] = bitswanEditor
		dockerCompose["volumes"] = map[string]interface{}{
			"bitswan-editor-data": nil,
		}
	}

	var buf bytes.Buffer

	// Serialize the docker-compose data structure to YAML and write it to the file
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2) // Optional: Set indentation
	if err := encoder.Encode(dockerCompose); err != nil {
		return "", "", fmt.Errorf("failed to encode docker-compose data structure: %w", err)
	}

	return buf.String(), gitopsSecretToken, nil
}

func CreateCaddyDockerComposeFile(caddyPath, domain string) (string, error) {
	caddyVolumes := []string{
		caddyPath + "/Caddyfile:/etc/caddy/Caddyfile",
		caddyPath + "/data:/data",
		caddyPath + "/config:/config",
		caddyPath + "/certs:/tls",
	}

	// Construct the docker-compose data structure
	dockerCompose := map[string]interface{}{
		"version": "3.8",
		"services": map[string]interface{}{
			"caddy": map[string]interface{}{
				"image":          "caddy:2.9",
				"restart":        "always",
				"container_name": "caddy",
				"ports":          []string{"80:80", "443:443", "2019:2019"},
				"networks":       []string{"bitswan_network"},
				"volumes":        caddyVolumes,
				"entrypoint":     []string{"caddy", "run", "--resume", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"},
			},
		},
		"networks": map[string]interface{}{
			"bitswan_network": map[string]interface{}{
				"external": true,
			},
		},
	}

	var buf bytes.Buffer

	// Serialize the docker-compose data structure to YAML and write it to the file
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2) // Optional: Set indentation
	if err := encoder.Encode(dockerCompose); err != nil {
		return "", fmt.Errorf("failed to encode docker-compose data structure: %w", err)
	}

	return buf.String(), nil
}

type EditorConfig struct {
	BindAddress string `yaml:"bind-addr"`
	Auth        string `yaml:"auth"`
	Password    string `yaml:"password"`
	Cert        bool   `yaml:"cert"`
}

func GetEditorPassword(projectName, gitopsName string) (string, error) {
	// First, wait for the editor service to be ready by streaming logs
	if err := waitForEditorReady(projectName, gitopsName); err != nil {
		return "", fmt.Errorf("failed to wait for editor to be ready: %w", err)
	}

	// Once the editor is ready, get the password
	getBitswanEditorPasswordCom := exec.Command("docker", "exec", projectName+"-bitswan-editor-"+gitopsName+"-1", "cat", "/home/coder/.config/code-server/config.yaml")
	out, err := getBitswanEditorPasswordCom.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get Bitswan Editor password: %w", err)
	}

	var editorConfig EditorConfig
	if err := yaml.Unmarshal(out, &editorConfig); err != nil {
		return "", fmt.Errorf("failed to unmarshal editor config: %w", err)
	}

	return editorConfig.Password, nil
}

func waitForEditorReady(projectName, gitopsName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", "compose", "-p", gitopsName+"-site", "logs", "-f", "bitswan-editor-"+gitopsName)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start docker compose logs command: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	readyChan := make(chan struct{})

	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "HTTP server listening on") {
				close(readyChan)
				return
			}
		}
	}()

	select {
	case <-readyChan:
		// Server is ready, kill the log streaming process
		if err := cmd.Process.Kill(); err != nil {
			// Just log this error, don't fail the function
			fmt.Printf("Warning: failed to kill log streaming process: %v\n", err)
		}
		return nil
	case <-ctx.Done():
		// Timeout or cancellation
		if err := cmd.Process.Kill(); err != nil {
			fmt.Printf("Warning: failed to kill log streaming process: %v\n", err)
		}
		return fmt.Errorf("timeout waiting for editor server to be ready")
	}
}
