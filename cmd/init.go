package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
	"bytes"
	"syscall"

	"github.com/bitswan-space/bitswan-gitops-cli/internal/caddyapi"
	"github.com/bitswan-space/bitswan-gitops-cli/internal/dockercompose"
	"github.com/bitswan-space/bitswan-gitops-cli/internal/dockerhub"
	"github.com/spf13/cobra"
)

type initOptions struct {
	remoteRepo string
	domain     string
	certsDir   string
	mkCerts    bool
	noIde      bool
	gitopsImage string
	editorImage string
}

type DockerNetwork struct {
	Name      string `json:"Name"`
	ID        string `json:"ID"`
	CreatedAt string `json:"CreatedAt"`
	Driver    string `json:"Driver"`
	IPv6      string `json:"IPv6"`
	Internal  string `json:"Internal"`
	Labels    string `json:"Labels"`
	Scope     string `json:"Scope"`
}

func defaultInitOptions() *initOptions {
	return &initOptions{}
}

func newInitCmd() *cobra.Command {
	o := defaultInitOptions()

	cmd := &cobra.Command{
		Use:   "init [flags] <gitops-name>",
		Short: "Initializes a new GitOps, Caddy and Bitswan editor",
		Args:  cobra.RangeArgs(1, 2),
		RunE:  o.run,
	}

	cmd.Flags().StringVar(&o.remoteRepo, "remote", "", "The remote repository to clone")
	cmd.Flags().StringVar(&o.domain, "domain", "", "The domain to use for the Caddyfile")
	cmd.Flags().StringVar(&o.certsDir, "certs-dir", "", "The directory where the certificates are located")
	cmd.Flags().BoolVar(&o.noIde, "no-ide", false, "Do not start Bitswan Editor")
	cmd.Flags().BoolVar(&o.mkCerts, "mkcerts", false, "Automatically generate local certificates using the mkcerts utility")
	cmd.Flags().StringVar(&o.gitopsImage, "gitops-image", "", "Custom image for the gitops")
	cmd.Flags().StringVar(&o.editorImage, "editor-image", "", "Custom image for the editor")

	return cmd
}

func cleanup(dir string) {
	if err := os.RemoveAll(dir); err != nil {
		fmt.Printf("Failed to clean up directory %s: %s\n", dir, err)
	}
}

func checkNetworkExists(networkName string) (bool, error) {
	// Run docker network ls command with JSON format
	cmd := exec.Command("docker", "network", "ls", "--format=json")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("error running docker command: %v", err)
	}

	// Split output into lines
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")

	// Process each line
	for _, line := range lines {
		var network DockerNetwork
		if err := json.Unmarshal([]byte(line), &network); err != nil {
			return false, fmt.Errorf("error parsing JSON: %v", err)
		}

		if network.Name == networkName {
			return true, nil
		}
	}

	return false, nil
}

func changeOwnership(directory string, uid, gid uint32) error {
	// Change ownership of directory recursively
	chownCom := exec.Command("chown", "-R", fmt.Sprintf("%d:%d", uid, gid), directory)
	if err := chownCom.Run(); err != nil {
		// Check if directory already has correct ownership
		info, statErr := os.Stat(directory)
		if statErr != nil {
			return fmt.Errorf("failed to change ownership and check status: %w\n %w", err, statErr)
		}

		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			// Check if UID already matches desired UID
			if stat.Uid == uid {
				// Directory already has correct ownership, ignore error
				return nil
			}
			return fmt.Errorf("failed to change ownership of directory: %w", err)
		}
	}
	return nil
}

func generateWildcardCerts(domain string) (string, error) {
    // Create temporary directory
    tempDir, err := os.MkdirTemp("", "certs-*")
    if err != nil {
        return "", fmt.Errorf("failed to create temp directory: %w", err)
    }

    // Store current working directory
    originalDir, err := os.Getwd()
    if err != nil {
        return "", fmt.Errorf("failed to get current directory: %w", err)
    }

    // Change to temp directory
    if err := os.Chdir(tempDir); err != nil {
        return "", fmt.Errorf("failed to change to temp directory: %w", err)
    }

    // Ensure we change back to original directory when function returns
    defer os.Chdir(originalDir)

    // Generate wildcard certificate
    wildcardDomain := "*." + domain
    cmd := exec.Command("mkcert", wildcardDomain)
    if err := cmd.Run(); err != nil {
        return "", fmt.Errorf("failed to generate certificate: %w", err)
    }

    // Generate file names
    keyFile := fmt.Sprintf("_wildcard.%s-key.pem", domain)
    certFile := fmt.Sprintf("_wildcard.%s.pem", domain)

    // Rename files
    if err := os.Rename(keyFile, "private-key.pem"); err != nil {
        return "", fmt.Errorf("failed to rename key file: %w", err)
    }
    if err := os.Rename(certFile, "full-chain.pem"); err != nil {
        return "", fmt.Errorf("failed to rename cert file: %w", err)
    }

    return tempDir, nil
}

func (o *initOptions) run(cmd *cobra.Command, args []string) error {
	bitswanConfig := os.Getenv("HOME") + "/.config/bitswan/"

	if _, err := os.Stat(bitswanConfig); os.IsNotExist(err) {
		if err := os.MkdirAll(bitswanConfig, 0755); err != nil {
			return fmt.Errorf("failed to create BitSwan config directory: %w", err)
		}
	}

	// Init bitswan network
	networkName := "bitswan_network"
	exists, err := checkNetworkExists(networkName)
	if err != nil {
		panic(fmt.Errorf("Error checking network: %v\n", err))
	}

	if exists {
		fmt.Printf("Network '%s' exists\n", networkName)
	} else {
		createDockerNetworkCom := exec.Command("docker", "network", "create", "bitswan_network")
		fmt.Println("Creating BitSwan Docker network...")
		if err := createDockerNetworkCom.Run(); err != nil {
			if err.Error() == "exit status 1" {
				fmt.Println("BitSwan Docker network already exists!")
			} else {
				fmt.Printf("Failed to create BitSwan Docker network: %s\n", err.Error())
			}
		} else {
			fmt.Println("BitSwan Docker network created!")
		}
	}

	// Init shared Caddy if not exists
	caddyConfig := bitswanConfig + "caddy"
  caddyCertsDir := caddyConfig + "/certs"

	defer func() {
		if r := recover(); r != nil {
			fmt.Println(r)
			fmt.Println("Failed to start Caddy. Cleaning up...")
			cleanup(caddyConfig)
		}
	}()

	client := &http.Client{
		Timeout: 2 * time.Second,
	}
	resp, err := client.Get("http://localhost:2019")
	caddy_running := true
	if err != nil {
		caddy_running = false
	} else {
		defer resp.Body.Close()
	}

	if !caddy_running {
		fmt.Println("Setting up Caddy...")
		if err := os.MkdirAll(caddyConfig, 0755); err != nil {
			return fmt.Errorf("failed to create Caddy config directory: %w", err)
		}

		// Create Caddyfile with email and modify admin listener
		caddyfile := `
		{
			email info@bitswan.space
			admin 0.0.0.0:2019
		}`

		caddyfilePath := caddyConfig + "/Caddyfile"
		if err := os.WriteFile(caddyfilePath, []byte(caddyfile), 0755); err != nil {
			panic(fmt.Errorf("Failed to write Caddyfile: %w", err))
		}

		caddyDockerCompose, err := dockercompose.CreateCaddyDockerComposeFile(caddyConfig, o.domain)
		if err != nil {
			panic(fmt.Errorf("Failed to create Caddy docker-compose file: %w", err))
		}

		caddyDockerComposePath := caddyConfig + "/docker-compose.yml"
		if err := os.WriteFile(caddyDockerComposePath, []byte(caddyDockerCompose), 0755); err != nil {
			panic(fmt.Errorf("Failed to write Caddy docker-compose file: %w", err))
		}

		err = os.Chdir(caddyConfig)
		if err != nil {
			panic(fmt.Errorf("Failed to change directory to Caddy config: %w", err))
		}

		caddyProjectName := "bitswan-caddy"
		caddyDockerComposeCom := exec.Command("docker", "compose", "-p", caddyProjectName, "up", "-d")

		// Capture both stdout and stderr
		var stdout, stderr bytes.Buffer
		caddyDockerComposeCom.Stdout = &stdout
		caddyDockerComposeCom.Stderr = &stderr

		// Create certs directory if it doesn't exist
		if _, err := os.Stat(caddyCertsDir); os.IsNotExist(err) {
			if err := os.MkdirAll(caddyCertsDir, 0740); err != nil {
        return fmt.Errorf("failed to create Caddy certs directory: %w", err)
			}
		}

		fmt.Println("Starting Caddy...")
		if err := caddyDockerComposeCom.Run(); err != nil {
			// Combine stdout and stderr for complete output
			fullOutput := stdout.String() + stderr.String()
			return fmt.Errorf("Failed to start Caddy:\nError: %v\nOutput:\n%s", err, fullOutput)
		}

		// wait 5s to make sure Caddy is up
		time.Sleep(5 * time.Second)
		err = caddyapi.InitCaddy()
		if err != nil {
			panic(fmt.Errorf("Failed to init Caddy: %w", err))
		}

		fmt.Println("Caddy started successfully!")
	} else {
		fmt.Println("A running instance of Caddy with admin found")
	}

	inputCertsDir := o.certsDir

	if o.mkCerts {
    certDir, err := generateWildcardCerts(o.domain)
    if err != nil {
			return fmt.Errorf("Error generating certificates: %v\n", err)
    }
		inputCertsDir = certDir
	}

	if inputCertsDir != "" {
		fmt.Println("Installing certs from", inputCertsDir)
		caddyCertsDir := caddyConfig + "/certs"
		if _, err := os.Stat(caddyCertsDir); os.IsNotExist(err) {
			if err := os.MkdirAll(caddyCertsDir, 0755); err != nil {
				return fmt.Errorf("failed to create Caddy certs directory: %w", err)
			}
		}

		certsDir := caddyCertsDir + "/" + o.domain
		if _, err := os.Stat(certsDir); os.IsNotExist(err) {
			if err := os.MkdirAll(certsDir, 0755); err != nil {
				return fmt.Errorf("failed to create certs directory: %w", err)
			}
		}

		certs, err := os.ReadDir(inputCertsDir)
		if err != nil {
			panic(fmt.Errorf("Failed to read certs directory: %w", err))
		}

		for _, cert := range certs {
			if cert.IsDir() {
				continue
			}

			certPath := inputCertsDir + "/" + cert.Name()
			newCertPath := certsDir + "/" + cert.Name()

			bytes, err := os.ReadFile(certPath)
			if err != nil {
				panic(fmt.Errorf("Failed to read cert file: %w", err))
			}

			if err := os.WriteFile(newCertPath, bytes, 0755); err != nil {
				panic(fmt.Errorf("Failed to copy cert file: %w", err))
			}
		}

		fmt.Println("Certs copied successfully!")
}

	// GitOps name
	gitopsName := "gitops"
	if len(args) == 1 {
		gitopsName = args[0]
	}

	gitopsConfig := bitswanConfig + gitopsName

	defer func() {
		if r := recover(); r != nil {
			fmt.Println(r)
			fmt.Println("Failed to initialize GitOps. Cleaning up...")
			cleanup(gitopsConfig)
		}
	}()

	if _, err := os.Stat(gitopsConfig); !os.IsNotExist(err) {
		return fmt.Errorf("GitOps with this name was already initialized: %s", gitopsName)
	}

	if err := os.MkdirAll(gitopsConfig, 0755); err != nil {
		return fmt.Errorf("failed to create GitOps directory: %w", err)
	}

	// Initialize Bitswan workspace
	gitopsWorkspace := gitopsConfig + "/workspace"
	if o.remoteRepo != "" {
		com := exec.Command("git", "clone", o.remoteRepo, gitopsWorkspace) //nolint:gosec

		fmt.Println("Cloning remote repository...")
		if err := com.Run(); err != nil {
			panic(fmt.Errorf("Failed to clone remote repository: %w", err))
		}
		fmt.Println("Remote repository cloned!")
	} else {
		if err := os.Mkdir(gitopsWorkspace, 0755); err != nil {
			return fmt.Errorf("failed to create GitOps workspace directory %s: %w", gitopsWorkspace, err)
		}
		com := exec.Command("git", "init")
		com.Dir = gitopsWorkspace

		fmt.Println("Initializing git in workspace...")

		if err := com.Run(); err != nil {
			panic(fmt.Errorf("Failed to init git in workspace: %w", err))
		}

		fmt.Println("Git initialized in workspace!")
	}

	if err := changeOwnership(gitopsWorkspace, 1000, 1000); err != nil {
    return err
	}

	// Add GitOps worktree
	gitopsWorktree := gitopsConfig + "/gitops"
	worktreeAddCom := exec.Command("git", "worktree", "add", "--orphan", "-b", gitopsName, gitopsWorktree)
	worktreeAddCom.Dir = gitopsWorkspace

	fmt.Println("Setting up GitOps worktree...")
	if err := worktreeAddCom.Run(); err != nil {
		panic(fmt.Errorf("Failed to create GitOps worktree: exit code %w.", err))
	}

	// Add repo as safe directory
	safeDirCom := exec.Command("git", "config", "--global", "--add", "safe.directory", gitopsWorktree)
	if err := safeDirCom.Run(); err != nil {
		panic(fmt.Errorf("Failed to add safe directory: %w", err))
	}

	if o.remoteRepo != "" {
		// Create empty commit
		emptyCommitCom := exec.Command("git", "commit", "--allow-empty", "-m", "Initial commit")
		emptyCommitCom.Dir = gitopsWorktree
		if err := emptyCommitCom.Run(); err != nil {
			panic(fmt.Errorf("Failed to create empty commit: %w", err))
		}

		// Push to remote
		setUpstreamCom := exec.Command("git", "push", "-u", "origin", gitopsName)
		setUpstreamCom.Dir = gitopsWorktree
		if err := setUpstreamCom.Run(); err != nil {
			panic(fmt.Errorf("Failed to set upstream: %w", err))
		}
	}

	fmt.Println("GitOps worktree set up successfully!")

	// Create secrets directory
	secretsDir := gitopsConfig + "/secrets"
	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		return fmt.Errorf("failed to create secrets directory: %w", err)
	}

	if err := changeOwnership(secretsDir, 1000, 1000); err != nil {
    return err
	}

	gitopsImage := o.gitopsImage
	if gitopsImage == "" {
		gitopsLatestVersion, err := dockerhub.GetLatestDockerHubVersion("https://hub.docker.com/v2/repositories/bitswan/gitops/tags/")
		if err != nil {
			panic(fmt.Errorf("Failed to get latest BitSwan GitOps version: %w", err))
		}
		gitopsImage = "bitswan/gitops:" + gitopsLatestVersion
	}

	bitswanEditorImage := o.editorImage
	if bitswanEditorImage == "" {
		bitswanEditorLatestVersion, err := dockerhub.GetLatestDockerHubVersion("https://hub.docker.com/v2/repositories/bitswan/bitswan-editor/tags/")
		if err != nil {
			panic(fmt.Errorf("Failed to get latest BitSwan Editor version: %w", err))
		}
		bitswanEditorImage = "bitswan/bitswan-editor:" + bitswanEditorLatestVersion
	}

	fmt.Println("Setting up GitOps deployment...")
	gitopsDeployment := gitopsConfig + "/deployment"
	if err := os.MkdirAll(gitopsDeployment, 0755); err != nil {
		return fmt.Errorf("Failed to create deployment directory: %w", err)
	}

	err = caddyapi.AddCaddyRecords(gitopsName, o.domain, inputCertsDir != "", o.noIde)
	if err != nil {
		panic(fmt.Errorf("Failed to add Caddy records: %w", err))
	}

	compose, token, err := dockercompose.CreateDockerComposeFile(
		gitopsConfig,
		gitopsName,
		gitopsImage,
		bitswanEditorImage,
		o.domain,
		o.noIde,
	)
	if err != nil {
		panic(fmt.Errorf("Failed to create docker-compose file: %w", err))
	}

	dockerComposePath := gitopsDeployment + "/docker-compose.yml"
	if err := os.WriteFile(dockerComposePath, []byte(compose), 0755); err != nil {
		panic(fmt.Errorf("Failed to write docker-compose file: %w", err))
	}

	err = os.Chdir(gitopsDeployment)
	if err != nil {
		panic(fmt.Errorf("Failed to change directory to GitOps deployment: %w", err))
	}

	fmt.Println("GitOps deployment set up successfully!")

	projectName := gitopsName + "-site"
	dockerComposeCom := exec.Command("docker", "compose", "-p", projectName, "up", "-d")

	// Capture both stdout and stderr
	var stdout, stderr bytes.Buffer
	dockerComposeCom.Stdout = &stdout
	dockerComposeCom.Stderr = &stderr

	fmt.Println("Starting BitSwan GitOps...")
	if err := dockerComposeCom.Run(); err != nil {
    // Print the captured output
    if stdout.Len() > 0 {
			fmt.Printf("Command output:\n%s\n", stdout.String())
    }
    if stderr.Len() > 0 {
			fmt.Printf("Error output:\n%s\n", stderr.String())
    }
    panic(fmt.Errorf("failed to start docker-compose: %w", err))
	}

	fmt.Println("BitSwan GitOps initialized successfully!")

	// Get Bitswan Editor password from container
	if !o.noIde {
		editorPassword, err := dockercompose.GetEditorPassword(projectName, gitopsName)
		if err != nil {
			panic(fmt.Errorf("Failed to get Bitswan Editor password: %w", err))
		}
		fmt.Println("------------BITSWAN EDITOR INFO------------")
		fmt.Printf("Bitswan Editor URL: https://editor.%s\n", o.domain)
		fmt.Printf("Bitswan Editor Password: %s\n", editorPassword)
	}

	fmt.Println("------------GITOPS INFO------------")
	fmt.Printf("GitOps ID: %s\n", gitopsName)
	fmt.Printf("GitOps URL: https://%s\n", o.domain)
	fmt.Printf("GitOps Secret: %s\n", token)

	return nil
}
