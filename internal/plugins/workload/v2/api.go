// Copyright 2024 Nukleros
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"sigs.k8s.io/kubebuilder/v4/pkg/config"
	"sigs.k8s.io/kubebuilder/v4/pkg/machinery"
	"sigs.k8s.io/kubebuilder/v4/pkg/model/resource"
	"sigs.k8s.io/kubebuilder/v4/pkg/plugin"
	"sigs.k8s.io/kubebuilder/v4/pkg/plugin/util"
	goplugin "sigs.k8s.io/kubebuilder/v4/pkg/plugins/golang"

	"github.com/nukleros/operator-builder/internal/controllergen"
	"github.com/nukleros/operator-builder/internal/plugins/workload"
	"github.com/nukleros/operator-builder/internal/plugins/workload/v2/scaffolds"
	scaffoldtmpls "github.com/nukleros/operator-builder/internal/plugins/workload/v2/scaffolds/templates"
	"github.com/nukleros/operator-builder/internal/utils"
	"github.com/nukleros/operator-builder/internal/workload/v1/commands/subcommand"
	workloadconfig "github.com/nukleros/operator-builder/internal/workload/v1/config"
	"github.com/nukleros/operator-builder/internal/workload/v1/kinds"
)

type createAPISubcommand struct {
	config   config.Config
	options  *goplugin.Options
	resource *resource.Resource

	force             bool
	generateDeepCopy  bool
	generateManifests bool

	// Check if we have to scaffold resource and/or controller
	resourceFlag   *pflag.Flag
	controllerFlag *pflag.Flag

	workloadConfigPath string
	cliRootCommandName string
	workload           kinds.WorkloadBuilder
	enableOlm          bool
	controllerImg      string // preserved from init for --makefile / --readme

	// init-level file update flags (all default false; written once at init time)
	updateGomod      bool
	updateDockerfile bool
	updateLint       bool
	updateMakefile   bool
	updateReadme     bool
}

var (
	ErrScaffoldCreateAPI = errors.New("unable to scaffold api")
	ErrAPIResourceExists = errors.New("API resource already exists")
	ErrMissingRootFile   = errors.New("file should present in the root directory")
)

var _ plugin.CreateAPISubcommand = &createAPISubcommand{}

func (p *createAPISubcommand) UpdateMetadata(cliMeta plugin.CLIMetadata, subcmdMeta *plugin.SubcommandMetadata) {
	subcmdMeta.Description = `Build a new API that can capture state for workloads
`
	subcmdMeta.Examples = fmt.Sprintf(`  # Add API attributes defined by a workload config file
  %[1]s create api --workload-config .source-manifests/workload.yaml
`, cliMeta.CommandName)
}

func (p *createAPISubcommand) BindFlags(fs *pflag.FlagSet) {
	workload.AddFlags(fs, &p.workloadConfigPath, &p.enableOlm)

	fs.BoolVar(&p.generateDeepCopy, "generate-deep-copy", true,
		"if true, generate deep copy methods after scaffolding (equivalent of 'make generate')")
	fs.BoolVar(&p.generateManifests, "generate-manifests", true,
		"if true, generate manifests and crds after scaffolding (equivalent of 'make manifests')")
	fs.BoolVar(&p.force, "force", false,
		"attempt to create resource even if it already exists")

	p.options = &goplugin.Options{}

	fs.StringVar(&p.options.Plural, "plural", "", "resource irregular plural form")
	fs.BoolVar(&p.options.DoAPI, "resource", true,
		"if set, generate the resource without prompting the user")
	p.resourceFlag = fs.Lookup("resource")
	fs.BoolVar(&p.options.Namespaced, "namespaced", true, "resource is namespaced")
	fs.BoolVar(&p.options.DoController, "controller", true,
		"if set, generate the controller without prompting the user")
	p.controllerFlag = fs.Lookup("controller")

	// init-level file update flags — regenerate files that are normally written
	// only once at project initialisation.
	fs.BoolVar(&p.updateGomod, "gomod", false, "update the go.mod file from the latest template")
	fs.BoolVar(&p.updateDockerfile, "dockerfile", false, "update the Dockerfile from the latest template")
	fs.BoolVar(&p.updateLint, "lint", false, "update .golangci.yml from the latest template")
	fs.BoolVar(&p.updateMakefile, "makefile", false, "update the Makefile from the latest template")
	fs.BoolVar(&p.updateReadme, "readme", false, "update README.md from the latest template")
}

func (p *createAPISubcommand) InjectConfig(c config.Config) error {
	// Read the stored plugin config to preserve the controller image that was
	// set at init time — needed by --makefile and --readme without requiring
	// the user to re-supply it.
	var stored workloadconfig.Plugin
	if err := c.DecodePluginConfig(workloadconfig.PluginKey, &stored); err == nil {
		p.controllerImg = stored.ControllerImg
	}

	processor, err := workloadconfig.Parse(p.workloadConfigPath)
	if err != nil {
		return fmt.Errorf("unable to inject config into %s, %w", p.workloadConfigPath, err)
	}

	p.workload = processor.Workload
	p.cliRootCommandName = p.workload.GetRootCommand().Name

	pluginConfig := workloadconfig.Plugin{
		WorkloadConfigPath: p.workloadConfigPath,
		CliRootCommandName: p.cliRootCommandName,
		ControllerImg:      p.controllerImg,
		EnableOLM:          p.enableOlm,
	}

	if err := c.EncodePluginConfig(workloadconfig.PluginKey, pluginConfig); err != nil {
		return fmt.Errorf("unable to encode plugin config at key %s, %w", workloadconfig.PluginKey, err)
	}

	p.config = c

	return nil
}

func (p *createAPISubcommand) InjectResource(res *resource.Resource) error {
	// set from config file if not provided with command line flag
	if res.Group == "" {
		res.Group = p.workload.GetAPIGroup()
	}

	if res.Version == "" {
		res.Version = p.workload.GetAPIVersion()
	}

	if res.Kind == "" {
		res.Kind = p.workload.GetAPIKind()
		res.Plural = resource.RegularPlural(p.workload.GetAPIKind())
	}

	// TODO: re-evaluate whether y/n input still makes sense. We should probably always
	//       scaffold the resource and controller.
	// Ask for API and Controller if not specified
	reader := bufio.NewReader(os.Stdin)
	if !p.resourceFlag.Changed {
		log.Println("Create Resource [y/n]")
		p.options.DoAPI = util.YesNo(reader)
	}
	if !p.controllerFlag.Changed {
		log.Println("Create Controller [y/n]")
		p.options.DoController = util.YesNo(reader)
	}

	p.options.UpdateResource(res, p.config)
	res.Path = path.Join(p.config.GetRepository(), "apis", res.Group, res.Version)

	if err := res.Validate(); err != nil {
		return err
	}

	// In case we want to scaffold a resource API we need to do some checks
	if p.options.DoAPI {
		// Check that resource doesn't have the API scaffolded or flag force was set
		if r, err := p.config.GetResource(res.GVK); err == nil && r.HasAPI() && !p.force {
			return ErrAPIResourceExists
		}
	}

	if !p.config.HasResource(res.GVK) {
		if err := p.config.AddResource(*res); err != nil {
			return fmt.Errorf("unable to add resource to config, %w", err)
		}
	}

	p.resource = res

	return nil
}

func (p *createAPISubcommand) PreScaffold(machinery.Filesystem) error {
	processor, err := workloadconfig.Parse(p.workloadConfigPath)
	if err != nil {
		return fmt.Errorf("%s for %s, %w", ErrScaffoldCreateAPI.Error(), p.workloadConfigPath, err)
	}

	if err := subcommand.CreateAPI(processor); err != nil {
		return fmt.Errorf("%s for %s, %w", ErrScaffoldCreateAPI.Error(), p.workloadConfigPath, err)
	}

	// check if main.go is present in the root directory
	if _, err := os.Stat(utils.DefaultMainPath); os.IsNotExist(err) {
		return fmt.Errorf("missing file [%s], %w", utils.DefaultMainPath, ErrMissingRootFile)
	}

	p.workload = processor.Workload

	return nil
}

func (p *createAPISubcommand) Scaffold(fs machinery.Filesystem) error {
	scaffolder := scaffolds.NewAPIScaffolder(
		p.config,
		p.resource,
		p.workload,
		p.cliRootCommandName,
		p.enableOlm,
	)
	scaffolder.InjectFS(fs)

	if err := scaffolder.Scaffold(); err != nil {
		return fmt.Errorf("%s for %s, %w", ErrScaffoldInit.Error(), p.workloadConfigPath, err)
	}

	return p.scaffoldInitFiles(fs)
}

// scaffoldInitFiles overwrites init-level project files when the corresponding
// flags are set.  These files are normally written once at `init` time; the
// flags allow regenerating them from the latest templates without running a
// full project initialisation.
func (p *createAPISubcommand) scaffoldInitFiles(fs machinery.Filesystem) error {
	var files []machinery.Builder

	if p.updateGomod {
		files = append(files, &scaffoldtmpls.GoMod{})
	}

	if p.updateDockerfile {
		files = append(files, &scaffoldtmpls.Dockerfile{})
	}

	if p.updateLint {
		files = append(files, &scaffoldtmpls.Golangci{Overwrite: true})
	}

	if p.updateMakefile {
		files = append(files, &scaffoldtmpls.Makefile{
			RootCmdName:              p.cliRootCommandName,
			ControllerImg:            p.controllerImg,
			EnableOLM:                p.enableOlm,
			KustomizeVersion:         utils.KustomizeVersion,
			ControllerToolsVersion:   utils.ControllerToolsVersion,
			OperatorSDKVersion:       utils.OperatorSDKVersion,
			ControllerRuntimeVersion: utils.ControllerRuntimeVersion,
			EnvtestVersion:           utils.EnvtestVersion,
			EnvtestK8SVersion:        utils.EnvtestK8SVersion,
			GolangCILintVersion:      utils.GolangCILintVersion,
		})
	}

	if p.updateReadme {
		files = append(files, &scaffoldtmpls.Readme{
			RootCmdName:   p.cliRootCommandName,
			EnableOLM:     p.enableOlm,
			ControllerImg: p.controllerImg,
		})
	}

	if len(files) == 0 {
		return nil
	}

	scaffold := machinery.NewScaffold(fs, machinery.WithConfig(p.config))

	return scaffold.Execute(files...)
}

func (p *createAPISubcommand) PostScaffold() error {
	err := util.RunCmd("Update dependencies", "go", "mod", "tidy")
	if err != nil {
		return err
	}

	// generate deep copy functions
	if p.generateDeepCopy && p.resource.HasAPI() {
		log.Println("Generating DeepCopy methods and other required functions...")

		generator, err := controllergen.NewObjectGenerator(controllergen.WithObjectGeneratorOptions("."))
		if err != nil {
			return fmt.Errorf("unable to create object generator, %w", err)
		}

		if err := generator.Execute(); err != nil {
			return fmt.Errorf("error in object generation, %w", err)
		}
	} else {
		log.Print("Next: generate DeepCopy and other required functions with:\n$ make generate\n")
	}

	// generate manifests
	if p.generateManifests && p.resource.HasAPI() {
		log.Println("Generating CRDs, RBAC, and controller manifests...")

		generator, err := controllergen.NewObjectGenerator(controllergen.WithManifestGeneratorOptions("."))
		if err != nil {
			return fmt.Errorf("unable to create manifest generator, %w", err)
		}

		if err := generator.Execute(); err != nil {
			return fmt.Errorf("error in manifest generation, %w", err)
		}
	} else {
		log.Print("Next: implement your new API and generate the manifests (e.g. CRDs,CRs) with:\n$ make manifests\n")
	}

	return nil
}
