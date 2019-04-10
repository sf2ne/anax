package dev

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	docker "github.com/fsouza/go-dockerclient"
	"github.com/open-horizon/anax/api"
	"github.com/open-horizon/anax/cli/cliutils"
	cliexchange "github.com/open-horizon/anax/cli/exchange"
	"github.com/open-horizon/anax/cli/register"
	"github.com/open-horizon/anax/config"
	"github.com/open-horizon/anax/container"
	"github.com/open-horizon/anax/containermessage"
	"github.com/open-horizon/anax/cutil"
	"github.com/open-horizon/anax/events"
	"github.com/open-horizon/anax/exchange"
	"github.com/open-horizon/anax/persistence"
	"github.com/open-horizon/anax/policy"
	"github.com/open-horizon/anax/torrent"
	"github.com/satori/go.uuid"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// Constants that hold the name of env vars used with the context of the hzn dev commands.
const DEVTOOL_HZN_ORG = "HZN_ORG_ID"
const DEVTOOL_HZN_USER = "HZN_EXCHANGE_USER_AUTH"
const DEVTOOL_HZN_EXCHANGE_URL = "HZN_EXCHANGE_URL"
const DEVTOOL_HZN_DEVICE_ID = "HZN_DEVICE_ID"
const DEVTOOL_HZN_PATTERN = "HZN_PATTERN"

const DEVTOOL_HZN_FSS_IMAGE_TAG = "HZN_DEV_FSS_IMAGE_TAG"
const DEVTOOL_HZN_FSS_CSS_PORT = "HZN_DEV_FSS_CSS_PORT"
const DEVTOOL_HZN_FSS_MONGO_IMAGE = "HZN_DEV_FSS_MONGO_IMAGE"
const DEVTOOL_HZN_FSS_WORKING_DIR = "HZN_DEV_FSS_WORKING_DIR"
const DEFAULT_DEVTOOL_HZN_FSS_WORKING_DIR = "/tmp/hzndev/"

const DEFAULT_WORKING_DIR = "horizon"
const DEFAULT_DEPENDENCY_DIR = "dependencies"

// The current working directory could be specified via input (as an absolute or relative path) or
// it could be defaulted if there is no input. If it must exist but does not, return an error.
func GetWorkingDir(dashD string, verifyExists bool) (string, error) {
	dir := dashD
	var err error
	if dir == "" {
		dir = DEFAULT_WORKING_DIR
	}

	if dir, err = filepath.Abs(dir); err != nil {
		return "", err
	} else if verifyExists {
		if _, err := os.Stat(dir); err != nil {
			return "", err
		}
	}

	return dir, nil
}

// Create the working directory if needed.
func CreateWorkingDir(dir string) error {
	// Create the working directory with the dependencies and pattern directories in one shot. If it already exists, just keep going.

	newDepDir := path.Join(dir, DEFAULT_DEPENDENCY_DIR)
	if _, err := os.Stat(newDepDir); os.IsNotExist(err) {
		if err := os.MkdirAll(newDepDir, 0755); err != nil {
			return errors.New(fmt.Sprintf("could not create directory %v, error: %v", newDepDir, err))
		}
	} else if err != nil {
		return errors.New(fmt.Sprintf("could not get status of directory %v, error: %v", newDepDir, err))
	}

	cliutils.Verbose("Using working directory: %v", dir)
	return nil
}

// Check for a file's existence or error out of the command. This is just a way to consolidate the error handling because
// we have several files that we're dealing with.
func FileNotExist(dir string, cmd string, fileName string, check func(string) (bool, error)) {
	if exists, err := check(dir); err != nil {
		cliutils.Fatal(cliutils.CLI_INPUT_ERROR, "'%v' %v", cmd, err)
	} else if exists {
		cliutils.Fatal(cliutils.CLI_INPUT_ERROR, "'%v' %v", cmd, fmt.Sprintf("horizon project in %v already contains %v.", dir, fileName))
	}
}

// Check for file existence and return any errors.
func FileExists(directory string, fileName string) (bool, error) {
	filePath := path.Join(directory, fileName)
	if _, err := os.Stat(filePath); err != nil && !os.IsNotExist(err) {
		return false, errors.New(fmt.Sprintf("error checking for %v: %v", fileName, err))
	} else if err == nil {
		return true, nil
	}
	return false, nil
}

// This function demarshals the file bytes into the input obj structure. The contents of what obj
// points to will be modified by this function.
func GetFile(directory string, fileName string, obj interface{}) error {
	filePath := path.Join(directory, fileName)

	fileBytes := cliutils.ReadJsonFile(filePath)
	if err := json.Unmarshal(fileBytes, obj); err != nil {
		return errors.New(fmt.Sprintf("failed to unmarshal %s, error: %v", filePath, err))
	}
	return nil
}

// This function takes one of the project json objects and writes it to a file in the project.
func CreateFile(directory string, fileName string, obj interface{}) error {
	// Convert the object to JSON and write it.
	filePath := path.Join(directory, fileName)
	if jsonBytes, err := json.MarshalIndent(obj, "", "    "); err != nil {
		return errors.New(fmt.Sprintf("failed to create json object for %v, error: %v", fileName, err))
	} else if err := ioutil.WriteFile(filePath, jsonBytes, 0664); err != nil {
		return errors.New(fmt.Sprintf("unable to write json object for %v to file %v, error: %v", fileName, filePath, err))
	} else {
		return nil
	}
}

// Common verification before executing a sub command.
func VerifyEnvironment(homeDirectory string, mustExist bool, needExchange bool, userCreds string) (string, error) {

	// Make sure the env vars needed by the dev tools are setup
	if needExchange && userCreds != "" {
		id, _ := cliutils.SplitIdToken(userCreds) // only look for the / in the id, because the token is more likely to have special chars
		if !strings.Contains(id, "/") && os.Getenv(DEVTOOL_HZN_ORG) == "" {
			return "", errors.New(fmt.Sprintf("Must set environment variable %v or specify the user as 'org/user' on the --user-pw flag", DEVTOOL_HZN_ORG))
		}
	} else if needExchange && userCreds == "" {
		id, _ := cliutils.SplitIdToken(os.Getenv(DEVTOOL_HZN_USER)) // only look for the / in the id, because the token is more likely to have special chars
		if !strings.Contains(id, "/") && os.Getenv(DEVTOOL_HZN_ORG) == "" {
			return "", errors.New(fmt.Sprintf("Must set environment variable %v or specify the user as 'org/user' on the --user-pw flag", DEVTOOL_HZN_ORG))
		}
	}

	if needExchange && os.Getenv(DEVTOOL_HZN_USER) == "" && userCreds == "" {
		return "", errors.New(fmt.Sprintf("Must set environment variable %v or specify user exchange credentials with --user-pw", DEVTOOL_HZN_USER))
	} else if os.Getenv(DEVTOOL_HZN_EXCHANGE_URL) == "" {
		exchangeUrl := cliutils.GetExchangeUrl()
		if exchangeUrl != "" {
			os.Setenv(DEVTOOL_HZN_EXCHANGE_URL, exchangeUrl)
		} else {
			return "", errors.New(fmt.Sprintf("Environment variable %v must be set.", DEVTOOL_HZN_EXCHANGE_URL))
		}
	}

	// Get the directory we're working in
	dir, err := GetWorkingDir(homeDirectory, mustExist)
	if err != nil {
		return "", errors.New(fmt.Sprintf("project has no horizon metadata directory. Use hzn dev to create a new project. Error: %v", err))
	} else {
		return dir, nil
	}

}

// Indicates whether or not the given project is a service project.
func IsServiceProject(directory string) bool {
	if ex, err := ServiceDefinitionExists(directory); !ex || err != nil {
		return false
	} else if ex, err := DependenciesExists(directory, true); !ex || err != nil {
		return false
	}
	return true
}

func CommonProjectValidation(dir string, userInputFile string, projectType string, cmd string) {
	// Get the Userinput file, so that we can validate it.
	userInputs, userInputsFilePath, uierr := GetUserInputs(dir, userInputFile)
	if uierr != nil {
		cliutils.Fatal(cliutils.CLI_INPUT_ERROR, "'%v %v' %v", projectType, cmd, uierr)
	}

	if verr := ValidateUserInput(userInputs, dir, userInputsFilePath, projectType); verr != nil {
		cliutils.Fatal(cliutils.CLI_INPUT_ERROR, "'%v %v' project does not validate. %v ", projectType, cmd, verr)
	}

	// Validate Dependencies
	if derr := ValidateDependencies(dir, userInputs, userInputsFilePath, projectType); derr != nil {
		cliutils.Fatal(cliutils.CLI_INPUT_ERROR, "'%v %v' project does not validate. %v", projectType, cmd, derr)
	}
}

// Validate that the input list of files actually exist.
func FileValidation(configFiles []string, configType string, projectType string, cmd string) []string {

	if len(configFiles) > 0 && configType == "" {
		cliutils.Fatal(cliutils.CLI_INPUT_ERROR, "'%v %v' Must specify configuration file type (-t) when a configuration file is specified (-m).", projectType, cmd)
	}

	absoluteFiles := make([]string, 0, 5)

	for _, fileRef := range configFiles {
		if absFileRef, err := filepath.Abs(fileRef); err != nil {
			cliutils.Fatal(cliutils.CLI_INPUT_ERROR, "'%v %v' configuration file %v error %v", projectType, cmd, fileRef, err)
		} else if _, err := os.Stat(absFileRef); err != nil {
			cliutils.Fatal(cliutils.CLI_INPUT_ERROR, "'%v %v' configuration file %v error %v", projectType, cmd, fileRef, err)
		} else {
			absoluteFiles = append(absoluteFiles, absFileRef)
		}
	}

	return absoluteFiles
}

func AbstractServiceValidation(dir string) error {
	if verr := ValidateServiceDefinition(dir, SERVICE_DEFINITION_FILE); verr != nil {
		return errors.New(fmt.Sprintf("project does not validate. %v ", verr))
	}
	return nil
}

// Sort of like a constructor, it creates an in memory object except that it is created from a service
// definition config file in the current project. This function assumes the caller has determined the exact location of the file.
// This function also assumes that the project pointed to by the directory parameter is assumed to contain the kind of definition
// the caller expects.
func GetAbstractDefinition(directory string) (cliexchange.AbstractServiceFile, error) {

	tryDefinitionName := SERVICE_DEFINITION_FILE
	res := new(cliexchange.ServiceFile)

	// GetFile will write to the res object, demarshalling the bytes into a json object that can be returned.
	if err := GetFile(directory, tryDefinitionName, res); err != nil {
		return nil, err
	}
	return res, nil

}

// Common setup processing for handling workload related commands.
func setup(homeDirectory string, mustExist bool, needExchange bool, userCreds string) (string, error) {

	// Shut off the Anax runtime logging.
	flag.Set("v", "0")

	// Verify that the environment and inputs are usable.
	dir, err := VerifyEnvironment(homeDirectory, mustExist, needExchange, userCreds)
	if err != nil {
		return "", err
	}

	cliutils.Verbose("Reading Horizon metadata from %s", dir)

	// Verify that the project is a service project.
	if !IsServiceProject(dir) {
		cliutils.Fatal(cliutils.CLI_INPUT_ERROR, "project in %v is not a horizon project.", dir)
	}

	return dir, nil
}

func makeByValueAttributes(attrs []persistence.Attribute) []persistence.Attribute {
	byValueAttrs := make([]persistence.Attribute, 0, 10)
	for _, a := range attrs {
		switch a.(type) {
		case *persistence.LocationAttributes:
			p := a.(*persistence.LocationAttributes)
			byValueAttrs = append(byValueAttrs, *p)
		case *persistence.ComputeAttributes:
			p := a.(*persistence.ComputeAttributes)
			byValueAttrs = append(byValueAttrs, *p)
		case *persistence.ArchitectureAttributes:
			p := a.(*persistence.ArchitectureAttributes)
			byValueAttrs = append(byValueAttrs, *p)
		case *persistence.HAAttributes:
			p := a.(*persistence.HAAttributes)
			byValueAttrs = append(byValueAttrs, *p)
		case *persistence.HTTPSBasicAuthAttributes:
			p := a.(*persistence.HTTPSBasicAuthAttributes)
			byValueAttrs = append(byValueAttrs, *p)
		case *persistence.DockerRegistryAuthAttributes:
			p := a.(*persistence.DockerRegistryAuthAttributes)
			byValueAttrs = append(byValueAttrs, *p)
		}
	}
	return byValueAttrs
}

// Create the environment variable map needed by the container worker to hold the environment variables that are passed to the
// workload container.
func createEnvVarMap(agreementId string,
	workloadPW string,
	global []register.GlobalSet,
	msURL string,
	configVar map[string]interface{},
	defaultVar []exchange.UserInput,
	org string,
	cw *container.ContainerWorker,
	attrConverter func(attributes []persistence.Attribute,
		envvars map[string]string,
		prefix string,
		defaultRAM int64) (map[string]string, error),
) (map[string]string, error) {

	// First, add in the Horizon platform env vars.
	envvars := make(map[string]string)

	// Set the env vars that will be passed to the services.
	cutil.SetPlatformEnvvars(envvars,
		config.ENVVAR_PREFIX,
		agreementId,
		GetNodeId(),
		org,
		workloadPW,
		os.Getenv(DEVTOOL_HZN_EXCHANGE_URL),
		os.Getenv(DEVTOOL_HZN_PATTERN),
		cw.Config.GetFileSyncServiceProtocol(),
		cw.Config.GetFileSyncServiceAPIListen(),
		strconv.Itoa(int(cw.Config.GetFileSyncServiceAPIPort())))

	// Second, add the Horizon system env vars. Some of these can come from the global section of a user inputs file. To do this we have to
	// convert the attributes in the userinput file into API attributes so that they can be validity checked. Then they are converted to
	// persistence attributes so that they can be further converted to environment variables. This is the progression that anax uses when
	// running real workloads so the same progression is used here.

	// The set of global attributes in the project's userinput file might not all be applicable to all services, so we will
	// create a shortened list of global attribute that only apply to this service.
	shortGlobals := make([]register.GlobalSet, 0, 10)
	for _, inputGlobal := range global {
		if len(inputGlobal.ServiceSpecs) == 0 || (inputGlobal.ServiceSpecs[0].Url == msURL && inputGlobal.ServiceSpecs[0].Org == org) {
			shortGlobals = append(shortGlobals, inputGlobal)
		}
	}

	// Now convert the reduced global attribute set to API attributes.
	attrs, err := GlobalSetAsAttributes(shortGlobals)
	if err != nil {
		return nil, errors.New(fmt.Sprintf("%v has error: %v ", USERINPUT_FILE, err))
	}

	// Third, add in default system attributes if not already present.
	attrs = api.FinalizeAttributesSpecifiedInService(1024, persistence.NewServiceSpec(msURL, org), attrs)

	cliutils.Verbose("Final Attributes: %v", attrs)

	// The conversion to persistent attributes produces an array of pointers to attributes, we need a by-value
	// array of attributes because that's what the functions which convert attributes to env vars expect. This is
	// because at runtime, the attributes are serialized to a database and then read out again before converting to env vars.

	byValueAttrs := makeByValueAttributes(attrs)

	// Fourth, convert all attributes to system env vars.
	var cerr error
	envvars, cerr = attrConverter(byValueAttrs, envvars, config.ENVVAR_PREFIX, cw.Config.Edge.DefaultServiceRegistrationRAM)
	if cerr != nil {
		return nil, errors.New(fmt.Sprintf("global attribute conversion error: %v", cerr))
	}

	// Last, now that the system and attribute based env vars are in place, we can convert the workload defined variables to env
	// vars and add them into the env var map.
	// Add in default variables from the workload definition.
	AddDefaultUserInputs(defaultVar, envvars)

	// Then add in the configured variable values from the workload section of the user input file.
	if err := AddConfiguredUserInputs(configVar, envvars); err != nil {
		return nil, err
	}

	return envvars, nil
}

func createContainerWorker() (*container.ContainerWorker, error) {

	workloadStorageDir := "/tmp/hzn"
	if err := os.MkdirAll(workloadStorageDir, 0755); err != nil {
		return nil, err
	}

	config := &config.HorizonConfig{
		Edge: config.Config{
			ServiceStorage:                workloadStorageDir,
			DefaultServiceRegistrationRAM: 0,
			FileSyncService: config.FSSConfig{
				AuthenticationPath: path.Join(GetDevWorkingDirectory(), "auth"),
				APIListen:          path.Join(GetDevWorkingDirectory(), "essapi.sock"),
				APIProtocol:        "unix",
			},
		},
		AgreementBot:  config.AGConfig{},
		Collaborators: config.Collaborators{},
	}

	return container.CreateCLIContainerWorker(config)
}

// This function is used to setup context to execute a service container.
func CommonExecutionSetup(homeDirectory string, userInputFile string, projectType string, cmd string) (string, *register.InputFile, *container.ContainerWorker) {

	// Get the setup info and context for running the command.
	dir, err := setup(homeDirectory, true, false, "")
	if err != nil {
		cliutils.Fatal(cliutils.CLI_INPUT_ERROR, "'%v %v' %v", projectType, cmd, err)
	}

	// Get the userinput file, so that we can get the userinput variables.
	userInputs, _, err := GetUserInputs(dir, userInputFile)
	if err != nil {
		cliutils.Fatal(cliutils.CLI_GENERAL_ERROR, "'%v %v' %v", projectType, cmd, err)
	}

	// Create the containerWorker
	cw, cerr := createContainerWorker()
	if cerr != nil {
		cliutils.Fatal(cliutils.CLI_GENERAL_ERROR, "'%v %v' unable to create Container Worker, %v", projectType, cmd, cerr)
	}

	return dir, userInputs, cw
}

func findContainers(serviceName string, cw *container.ContainerWorker) ([]docker.APIContainers, error) {
	dcService := docker.ListContainersOptions{
		All: true,
		Filters: map[string][]string{
			"label": []string{
				fmt.Sprintf("%v.service_name=%v", container.LABEL_PREFIX, serviceName),
			},
		},
	}

	containers, err := cw.GetClient().ListContainers(dcService)
	if err != nil {
		return nil, errors.New(fmt.Sprintf("unable to list containers, %v", err))
	}
	return containers, nil
}

func getContainerNetworks(depConfig *cliexchange.DeploymentConfig, cw *container.ContainerWorker) (map[string]docker.ContainerNetwork, error) {
	containerNetworks := map[string]docker.ContainerNetwork{}
	for serviceName, _ := range depConfig.Services {
		containers, err := findContainers(serviceName, cw)
		if err != nil {
			return nil, errors.New(fmt.Sprintf("unable to list existing containers: %v", err))
		}
		// Return the main network for this service. It will always be the network name
		// that matches the agreement_id label.
		for _, msc := range containers {
			if nw_name, ok := msc.Labels[container.LABEL_PREFIX+".agreement_id"]; ok {
				if nw, ok := msc.Networks.Networks[nw_name]; ok {
					containerNetworks[nw_name] = nw
					cliutils.Verbose("Found main network for service %v, %v", nw_name, nw)
				}
			}
		}
	}
	return containerNetworks, nil
}

func ProcessStartDependencies(dir string, deps []*cliexchange.ServiceFile, globals []register.GlobalSet, configUserInputs []register.MicroWork, cw *container.ContainerWorker) (map[string]docker.ContainerNetwork, error) {

	// Collect all the service networks that have to be connected to the caller's container.
	ms_networks := map[string]docker.ContainerNetwork{}

	for _, depDef := range deps {

		msn, startErr := startDependent(dir, depDef, globals, configUserInputs, cw)

		// If there were errors, cleanup any services that are already started.
		if startErr != nil {

			// Stop any services that might already be started.
			ServiceStopTest(dir)

			cliutils.Fatal(cliutils.CLI_GENERAL_ERROR, "'%v %v' %v for dependency %v", SERVICE_COMMAND, SERVICE_START_COMMAND, startErr, depDef.URL)

		} else {
			// Add the dependent's networks to the map.
			for netName, net := range msn {
				ms_networks[netName] = net
			}
		}

	}

	return ms_networks, nil
}

func startDependent(dir string,
	serviceDef *cliexchange.ServiceFile,
	globals []register.GlobalSet, // API attributes
	configUserInputs []register.MicroWork, // indicates configured variables
	cw *container.ContainerWorker) (map[string]docker.ContainerNetwork, error) {

	// The docker networks of any dependencies that the input service has.
	msNetworks := map[string]docker.ContainerNetwork{}

	// Work our way down the dependency tree. If the service we want to start has dependencies, recursively process them
	// until we get to a leaf node. Leaf node services are started first, parents are started last.
	if serviceDef.HasDependencies() {

		if deps, err := GetServiceDependencies(dir, serviceDef.RequiredServices); err != nil {
			return nil, errors.New(fmt.Sprintf("unable to retrieve dependency metadata: %v", err))
			// Start this service's dependencies
		} else if msn, err := ProcessStartDependencies(dir, deps, globals, configUserInputs, cw); err != nil {
			return nil, errors.New(fmt.Sprintf("unable to start dependencies: %v", err))
		} else {
			msNetworks = msn
		}
	}

	// Convert the deployment config into a full DeploymentDescription.
	depConfig, deployment, derr := serviceDef.ConvertToDeploymentDescription(false)
	if derr != nil {
		return nil, derr
	}

	// Start the service containers
	if !depConfig.HasAnyServices() {
		cliutils.Verbose("Skipping service because it has no deployment configuration: %v", depConfig)
		return msNetworks, nil
	} else {

		// If the service we need to start is a sharable singleton then it might already be started. If it is then just return
		// the networks associated with the containers.
		if serviceDef.Sharable == exchange.MS_SHARING_MODE_SINGLETON || serviceDef.Sharable == exchange.MS_SHARING_MODE_SINGLE {

			if containerNetworks, err := getContainerNetworks(depConfig, cw); err != nil {
				return nil, err
			} else if len(containerNetworks) > 0 {
				return containerNetworks, nil
			}

		}

		// Start the service containers. Make an instance id the same way the runtime makes them.
		id, err := uuid.NewV4()
		if err != nil {
			return nil, errors.New(fmt.Sprintf("unable to generate instance ID: %v", err))
		}

		sId := cutil.MakeMSInstanceKey(serviceDef.URL, serviceDef.Org, serviceDef.Version, id.String())

		return StartContainers(deployment, serviceDef.URL, serviceDef.Version, globals, serviceDef.UserInputs, configUserInputs, serviceDef.Org, depConfig, cw, msNetworks, true, false, sId)
	}
}

func StartContainers(deployment *containermessage.DeploymentDescription,
	specRef string,
	version string,
	globals []register.GlobalSet, // API attributes
	defUserInputs []exchange.UserInput, // indicates variable defaults
	configUserInputs []register.MicroWork, // indicates configured variables
	org string,
	dc *cliexchange.DeploymentConfig,
	cw *container.ContainerWorker,
	msNetworks map[string]docker.ContainerNetwork,
	service bool,
	agreementBased bool,
	id string) (map[string]docker.ContainerNetwork, error) {

	// Establish logging context
	logName := "microservice"
	if service {
		logName = "service"
	}

	agId := ""
	wlpw := ""
	if agreementBased {
		agId = id
		wlpw = "deprecated"
	}

	// Dependencies that require userinput variables to be set must have those variables set in the current userinput file,
	// which is either the input userinput file or the default userinput file from the current project.
	configVars := getConfiguredVariables(configUserInputs, specRef)

	// Now that we have the configured variables, turn everything into environment variables for the container.
	environmentAdditions, enverr := createEnvVarMap(agId, wlpw, globals, specRef, configVars, defUserInputs, org, cw, persistence.AttributesToEnvvarMap)
	if enverr != nil {
		return nil, errors.New(fmt.Sprintf("unable to create environment variables"))
	}

	cliutils.Verbose("Passing environment variables: %v", environmentAdditions)

	// Start the dpendent service

	fmt.Printf("Start %v: %v with instance id prefix %v\n", logName, dc.CLIString(), id)

	// Start the dependent service container.
	_, startErr := cw.ResourcesCreate(id, "", nil, deployment, []byte(""), environmentAdditions, msNetworks, cutil.FormOrgSpecUrl(cutil.NormalizeURL(specRef), org))
	if startErr != nil {
		return nil, errors.New(fmt.Sprintf("unable to start container using %v, error: %v", dc.CLIString(), startErr))
	}

	fmt.Printf("Running %v.\n", logName)

	// Locate the service network(s) and return them so that a workload/parent-service can be hooked in.
	return getContainerNetworks(dc, cw)
}

func ProcessStopDependencies(dir string, deps []*cliexchange.ServiceFile, cw *container.ContainerWorker) error {

	// Log the stopping of dependencies if there are any.
	if len(deps) != 0 {
		cliutils.Verbose("Stopping dependencies.")
	}

	for _, depDef := range deps {
		if err := stopDependent(dir, depDef, cw); err != nil {
			return err
		}
	}

	return nil
}

func stopDependent(dir string, serviceDef *cliexchange.ServiceFile, cw *container.ContainerWorker) error {

	// Convert the deployment config into a full DeploymentDescription.
	depConfig, _, derr := serviceDef.ConvertToDeploymentDescription(false)
	if derr != nil {
		return derr
	}

	// Stop the service containers
	if !depConfig.HasAnyServices() {
		fmt.Printf("Skipping service because it has no deployment configuration: %v\n", depConfig)
	} else if err := stopContainers(depConfig, cw, true); err != nil {
		return err
	}

	// Work our way down the dependency tree. If the service we want to stop has dependencies, recursively process them
	// until we get to a leaf node. Parents are stopped first, leaf nodes are stopped last.
	if serviceDef.HasDependencies() {

		if deps, err := GetServiceDependencies(dir, serviceDef.RequiredServices); err != nil {
			return errors.New(fmt.Sprintf("unable to retrieve dependency metadata: %v", err))
			// Stop this service's dependencies
		} else if err := ProcessStopDependencies(dir, deps, cw); err != nil {
			return errors.New(fmt.Sprintf("unable to stop dependencies: %v", err))
		}
	}

	return nil
}

func StopService(dc *cliexchange.DeploymentConfig, cw *container.ContainerWorker) error {
	return stopContainers(dc, cw, true)
}

func stopContainers(dc *cliexchange.DeploymentConfig, cw *container.ContainerWorker, service bool) error {

	// Establish logging context
	logName := "service"
	if !service {
		logName = "microservice"
	}

	// Stop each container in the deployment config.
	for serviceName, _ := range dc.Services {
		containers, err := findContainers(serviceName, cw)
		if err != nil {
			return errors.New(fmt.Sprintf("unable to list containers, %v", err))
		}

		cliutils.Verbose("Found containers %v", containers)

		// Locate the container and stop it.
		for _, c := range containers {
			msId := c.Labels[container.LABEL_PREFIX+".agreement_id"]
			fmt.Printf("Stop %v: %v with instance id prefix %v\n", logName, dc.CLIString(), msId)
			cw.ResourcesRemove([]string{msId})
		}
	}
	return nil
}

func getImageReferenceAsTorrent(serviceDef *exchange.ServiceDefinition) policy.Torrent {

	pip := make(policy.ImplementationPackage)
	cutil.CopyMap(serviceDef.ImageStore, pip)
	return pip.ConvertToTorrent()
}

// Get the images into the local docker server for services
func getContainerImages(containerConfig *events.ContainerConfig, pemFiles []string, currentUIs *register.InputFile) error {

	// Create a temporary anax config object to hold config for the shared runtime functions.
	cfg := &config.HorizonConfig{
		Edge: config.Config{
			TrustSystemCACerts:     true,
			TrustDockerAuthFromOrg: true,
			TorrentDir:             "/tmp",
		},
		AgreementBot:  config.AGConfig{},
		Collaborators: config.Collaborators{},
	}

	col, _ := config.NewCollaborators(*cfg)
	cfg.Collaborators = *col

	// Create a docker client so that we can convert the downloaded images into docker images.
	dockerEP := "unix:///var/run/docker.sock"
	client, derr := docker.NewClient(dockerEP)
	if derr != nil {
		return errors.New(fmt.Sprintf("failed to create docker client, error: %v", derr))
	}

	// This is the image server authentication configuration. First get any anax attributes and convert them into
	// anax attributes.
	attributes, err := GlobalSetAsAttributes(currentUIs.Global)
	if err != nil {
		return errors.New(fmt.Sprintf("failed to convert global attributes in %v, error: %v ", USERINPUT_FILE, err))
	}
	byValueAttrs := makeByValueAttributes(attributes)

	// Then extract the HTTPS authentication attributes.
	httpAuthAttrs := make(map[string]map[string]string, 0)
	dockerAuthConfigurations := make(map[string][]docker.AuthConfiguration, 0)
	authErr := torrent.ExtractAuthAttributes(byValueAttrs, httpAuthAttrs, dockerAuthConfigurations)
	if authErr != nil {
		return errors.New(fmt.Sprintf("failed to extract authentication attribute from %v, error: %v ", USERINPUT_FILE, err))
	}

	cliutils.Verbose("Using HTTPS Basic authorization: %v", httpAuthAttrs)

	fmt.Printf("getting container images into docker.\n")
	if err := torrent.ProcessImageFetch(cfg, client, containerConfig, httpAuthAttrs, dockerAuthConfigurations, pemFiles); err != nil {
		return errors.New(fmt.Sprintf("failed to get container images, error: %v", err))
	}

	return nil
}

func GetNodeId() string {
	// Allow device id override if the env var is set.
	testDeviceId, _ := os.Hostname()
	if os.Getenv(DEVTOOL_HZN_DEVICE_ID) != "" {
		testDeviceId = os.Getenv(DEVTOOL_HZN_DEVICE_ID)
	}
	return testDeviceId
}

func GetDevWorkingDirectory() string {
	wd := os.Getenv(DEVTOOL_HZN_FSS_WORKING_DIR)
	if wd == "" {
		wd = DEFAULT_DEVTOOL_HZN_FSS_WORKING_DIR
	}
	return wd
}

// It is used by "hzn dev service new" when the specRef is an empty string.
// This function generates a service specRef and version from the image name provided by the user.
func GetServiceSpecFromImage(image string) (string, string, error) {
	if image == "" {
		return "", "", nil
	}

	specRef := ""
	version := ""

	// parse the image
	_, path, tag, _ := cutil.ParseDockerImagePath(image)
	if path == "" {
		return "", "", errors.New(fmt.Sprintf("invalid image format: %v", image))
	} else {
		// get last part as the service ref
		s := strings.Split(path, "/")
		specRef = s[len(s)-1]
	}

	if tag != "" && policy.IsVersionString(tag) {
		version = tag
	}

	return specRef, version, nil
}

// This function extracts the image names from the image list and returns a map of name~image pairs.
// If the image does not have version tag specified, this function will add $SERVICE_VERSION as the tag
// so that it's easy for the user to update the version later. And it will append_$ARCH to the image name so
// distiguash images from different arch.
func GetImageInfoFromImageList(images []string, version string, noImageGen bool) (map[string]string, string, error) {
	imageInfo := make(map[string]string)
	image_base := ""

	if len(images) == 0 {
		imageInfo["$SERVICE_NAME"] = "${DOCKER_IMAGE_BASE}_$ARCH:$SERVICE_VERSION"
		return imageInfo, image_base, nil
	}

	for _, image := range images {
		host, path, tag, digest := cutil.ParseDockerImagePath(image)
		if path == "" {
			return nil, "", errors.New(fmt.Sprintf("invalid image format: %v", image))
		}
		s := strings.Split(path, "/")

		if !noImageGen {
			// only one image will be specified if noImageGen is flase.
			// In this case, remove the tag and digest, use SERVICE_VERSION for tag
			// The real image name will be "${DOCKER_IMAGE_BASE}_$ARCH:$SERVICE_VERSION"
			imageInfo[s[len(s)-1]] = "${DOCKER_IMAGE_BASE}_$ARCH:$SERVICE_VERSION"
			image_base = cutil.FormDockerImageName(host, path, "", "")
			return imageInfo, image_base, nil
		} else {
			if tag == "" && digest == "" {
				if len(images) == 1 {
					imageInfo[s[len(s)-1]] = "${DOCKER_IMAGE_BASE}_$ARCH:$SERVICE_VERSION"
					image_base = cutil.FormDockerImageName(host, path, "", "")
					return imageInfo, image_base, nil
				} else {
					// append _$ARCH in the image name and add SERVICE_VERSION as tag if the tag is not specified or the tag equals to the service version
					image = cutil.FormDockerImageName(host, fmt.Sprintf("%v_$ARCH", path), "$SERVICE_VERSION", "")
				}
			}
		}
		imageInfo[s[len(s)-1]] = image
	}

	return imageInfo, "", nil
}

// check the file existance, substitute the variables and save the file
func CreateFileWithConent(directory string, filename string, content string, substitutes map[string]string, perm_exec bool) error {

	filePath := path.Join(directory, filename)

	// make sure the file does not exist
	found, err := FileExists(directory, filename)
	if err != nil {
		return err
	}
	if found {
		return errors.New(fmt.Sprintf("file %v exists already", filePath))
	}

	// do the substitution
	if substitutes != nil {
		for key, val := range substitutes {
			content = strings.Replace(content, key, val, -1)
		}
	}

	// save the file
	var perm os.FileMode
	if perm_exec {
		// executable file
		perm = 0755
	} else {
		// regular file
		perm = 0644
	}
	if err := ioutil.WriteFile(filePath, []byte(content), perm); err != nil {
		return errors.New(fmt.Sprintf("unable to write content to file %v, error: %v", filePath, err))
	} else {
		return nil
	}
}
