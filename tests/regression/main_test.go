//go:build regression

package regression

import (
	"fmt"
	"os"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"
)

// testNomadAddr is the HTTP address of the Nomad cluster under test.
var testNomadAddr string

// testNomadClient is an API client connected to testNomadAddr.
// Use testNomadClient.Jobs() for test setup (register, deregister).
var testNomadClient *nomadapi.Client

// testNomadVersion records the Nomad version under test (informational).
var testNomadVersion string

// testBinaryPath is the path to the compiled nomad-botherer binary.
// It is empty when the build failed; E2E tests skip themselves in that case.
var testBinaryPath string

// nomadSDKEnvVars is the full set of env vars that nomadapi.DefaultConfig()
// reads automatically. We snapshot and clear these at startup so that
// subprocesses spawned by tests (the E2E binary, Docker commands) cannot
// accidentally reach a cluster that was configured in the caller's shell.
var nomadSDKEnvVars = []string{
	"NOMAD_ADDR",
	"NOMAD_TOKEN",
	"NOMAD_NAMESPACE",
	"NOMAD_REGION",
	"NOMAD_HTTP_AUTH",
	"NOMAD_CACERT",
	"NOMAD_CAPATH",
	"NOMAD_CLIENT_CERT",
	"NOMAD_CLIENT_KEY",
	"NOMAD_SKIP_VERIFY",
	"NOMAD_TLS_SERVER_NAME",
}

func TestMain(m *testing.M) {
	// Snapshot and immediately clear all Nomad SDK env vars. Tests that need
	// cluster config use the captured values directly; subprocesses inherit a
	// clean environment and are configured only via explicit CLI flags.
	savedEnv := make(map[string]string)
	for _, k := range nomadSDKEnvVars {
		if v, ok := os.LookupEnv(k); ok {
			savedEnv[k] = v
			os.Unsetenv(k)
		}
	}
	restoreEnv := func() {
		for k, v := range savedEnv {
			os.Setenv(k, v)
		}
	}

	// Read cluster config from the snapshot, not from the (now-cleared) env.
	nomadAddrFromEnv := savedEnv["NOMAD_ADDR"]
	nomadTokenFromEnv := savedEnv["NOMAD_TOKEN"]

	var cleanup func()
	var err error

	switch {
	case nomadAddrFromEnv != "":
		testNomadAddr = nomadAddrFromEnv
		testNomadVersion = os.Getenv("NOMAD_VERSION") // test-only var, not an SDK var
		cleanup = func() {}

	default:
		ver := os.Getenv("NOMAD_VERSION")
		if ver == "" {
			ver = defaultNomadVersion
		}
		testNomadVersion = ver
		testNomadAddr, cleanup, err = startNomadDocker(ver)
		if err != nil {
			fmt.Fprintf(os.Stderr, "regression: cannot start Nomad %s via Docker: %v\n", ver, err)
			fmt.Fprintln(os.Stderr, "  Tip: set NOMAD_ADDR to point at an existing cluster.")
			fmt.Fprintln(os.Stderr, "  Tip: set NOMAD_VERSION to pull a different image.")
			restoreEnv()
			os.Exit(1)
		}
	}

	// Build the client with explicit fields; DefaultConfig() already has no
	// env vars to read at this point.
	cfg := nomadapi.DefaultConfig()
	cfg.Address = testNomadAddr
	if nomadTokenFromEnv != "" {
		cfg.SecretID = nomadTokenFromEnv
	}
	testNomadClient, err = nomadapi.NewClient(cfg)
	if err != nil {
		cleanup()
		fmt.Fprintf(os.Stderr, "regression: nomad client: %v\n", err)
		restoreEnv()
		os.Exit(1)
	}

	testBinaryPath, err = buildBinary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "regression: build failed (E2E tests will be skipped): %v\n", err)
		testBinaryPath = ""
	}

	fmt.Printf("regression: Nomad %s at %s\n", testNomadVersion, testNomadAddr)
	code := m.Run()
	cleanup()
	if testBinaryPath != "" {
		os.Remove(testBinaryPath)
	}
	restoreEnv()
	os.Exit(code)
}
