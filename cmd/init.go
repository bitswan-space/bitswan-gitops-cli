package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/bitswan-space/bitswan-gitops-cli/internal/caddyapi"
	"github.com/bitswan-space/bitswan-gitops-cli/internal/dockercompose"
	"github.com/bitswan-space/bitswan-gitops-cli/internal/dockerhub"
	"github.com/spf13/cobra"
)



type initOptions struct {
	remoteRepo string
	domain     string
	certsDir   string
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

	return cmd
}

func cleanup(dir string) {
	if err := os.RemoveAll(dir); err != nil {
		fmt.Printf("Failed to clean up directory %s: %s\n", dir, err)
	}
}

func (o *initOptions) run(cmd *cobra.Command, args []string) error {
	bitswanConfig := os.Getenv("HOME") + "/.config/bitswan/"

	if _, err := os.Stat(bitswanConfig); os.IsNotExist(err) {
		if err := os.MkdirAll(bitswanConfig, 0755); err != nil {
			return fmt.Errorf("failed to create BitSwan config directory: %w", err)
		}
	}

	// Init shared Caddy if not exists
	caddyConfig := bitswanConfig + "caddy"

	defer func() {
		if r := recover(); r != nil {
			fmt.Println(r)
			fmt.Println("Failed to start Caddy. Cleaning up...")
			cleanup(caddyConfig)
		}
	}()

	if _, err := os.Stat(caddyConfig); os.IsNotExist(err) {
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

		caddyDockerCompose, err := dockercompose.CreateCaddyDockerComposeFile(caddyConfig, o.certsDir, o.domain)
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

		fmt.Println("Starting Caddy...")
		if err := caddyDockerComposeCom.Run(); err != nil {
			panic(fmt.Errorf("Failed to start Caddy: %w", err))
		}

		// wait 5s to make sure Caddy is up
		time.Sleep(5 * time.Second)
		err = caddyapi.InitCaddy()
		if err != nil {
			panic(fmt.Errorf("Failed to init Caddy: %w", err))
		}

		fmt.Println("Caddy started successfully!")
	}

	if o.certsDir != "" {
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

		certs, err := os.ReadDir(o.certsDir)
		if err != nil {
			panic(fmt.Errorf("Failed to read certs directory: %w", err))
		}

		for _, cert := range certs {
			if cert.IsDir() {
				continue
			}

			certPath := o.certsDir + "/" + cert.Name()
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

	// Change ownership of workspace folder recursively
	chownCom := exec.Command("chown", "-R", "1000:1000", gitopsWorkspace)
	if err := chownCom.Run(); err != nil {
		return fmt.Errorf("failed to change ownership of workspace folder: %w", err)
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
	if err := os.MkdirAll(secretsDir, 0660); err != nil {
		return fmt.Errorf("failed to create secrets directory: %w", err)
	}

	chownCom = exec.Command("chown", "-R", "1000:1000", secretsDir)
	if err := chownCom.Run(); err != nil {
		return fmt.Errorf("failed to change ownership of secrets folder: %w", err)
	}

	gitopsLatestVersion, err := dockerhub.GetLatestDockerHubVersion("https://hub.docker.com/v2/repositories/bitswan/gitops/tags/")
	if err != nil {
		panic(fmt.Errorf("Failed to get latest BitSwan GitOps version: %w", err))
	}

	bitswanEditorLatestVersion, err := dockerhub.GetLatestDockerHubVersion("https://hub.docker.com/v2/repositories/bitswan/bitswan-editor/tags/")
	if err != nil {
		panic(fmt.Errorf("Failed to get latest BitSwan Editor version: %w", err))
	}

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

	fmt.Println("Setting up GitOps deployment...")
	gitopsDeployment := gitopsConfig + "/deployment"
	if err := os.MkdirAll(gitopsDeployment, 0755); err != nil {
		return fmt.Errorf("Failed to create deployment directory: %w", err)
	}

	err = caddyapi.AddCaddyRecords(gitopsName, o.domain, o.certsDir != "")
	if err != nil {
		panic(fmt.Errorf("Failed to add Caddy records: %w", err))
	}

	compose, token, err := dockercompose.CreateDockerComposeFile(
		gitopsConfig,
		gitopsName,
		gitopsLatestVersion,
		bitswanEditorLatestVersion,
		o.certsDir,
		o.domain,
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

	fmt.Println("Starting BitSwan GitOps...")
	if err := dockerComposeCom.Run(); err != nil {
		panic(fmt.Errorf("failed to start docker-compose: %w", err))
	}

	fmt.Println("BitSwan GitOps initialized successfully!")

	// Get Bitswan Editor password from container

	editorPassword, err := dockercompose.GetEditorPassword(projectName, gitopsName)
	if err != nil {
		panic(fmt.Errorf("Failed to get Bitswan Editor password: %w", err))
	}

	fmt.Println("------------GITOPS INFO------------")
	fmt.Printf("GitOps ID: %s\n", gitopsName)
	fmt.Printf("GitOps URL: https://%s\n", o.domain)
	fmt.Printf("GitOps Secret: %s\n", token)

	fmt.Println("------------BITSWAN EDITOR INFO------------")
	fmt.Printf("Bitswan Editor URL: https://editor.%s\n", o.domain)
	fmt.Printf("Bitswan Editor Password: %s\n", editorPassword)


	return nil
}
