package snaplink

import (
	"fmt"
	"strings"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/pkg/sourceauth"
)

// Capabilities are explicit opt-ins for the Snaplink seams. Summary remains
// required for every enabled Snaplink connector; command and status methods
// are exposed only when their source contracts are configured and verified.
type Capabilities struct {
	Summary              bool
	Erase                bool
	OperationStatus      bool
	EraseContractVersion string
}

// The Snaplink erase contract is deliberately empty until the source owner
// supplies a verified, versioned contract. Test packages may add a fixture
// while exercising the adapter; production configuration cannot opt into an
// undocumented command version.
var eraseContractVersions = map[string]struct{}{}

// ValidateCapabilities validates capability/path relationships before any
// endpoint parsing, authentication setup, or HTTP client construction. It is
// shared by configuration loading and the adapter constructor so a dormant
// configuration cannot become active merely because a path was supplied.
func ValidateCapabilities(cfg connector.HTTPConfig, capabilities Capabilities) error {
	if err := validateCapabilityPaths(cfg); err != nil {
		return err
	}
	if err := validateCapabilityRelationships(cfg, capabilities); err != nil {
		return err
	}
	if err := validateEraseContract(capabilities); err != nil {
		return err
	}
	return validateCapabilityEndpoint(cfg, capabilities)
}

func validateCapabilityPaths(cfg connector.HTTPConfig) error {
	for field, path := range map[string]string{
		"summary_path": cfg.SummaryPath, "erase_path": cfg.ErasePath, "status_path": cfg.OperationStatusPath,
	} {
		if path != "" && !sourceauth.ValidAbsolutePath(path) {
			return fmt.Errorf("sources.snaplink.%s must be an absolute path without control characters", field)
		}
	}
	return nil
}

func validateCapabilityRelationships(cfg connector.HTTPConfig, capabilities Capabilities) error {
	if err := validateErasePathRelationship(cfg.ErasePath, capabilities.Erase); err != nil {
		return err
	}
	if err := validateStatusPathRelationship(cfg.OperationStatusPath, capabilities.OperationStatus); err != nil {
		return err
	}
	if strings.TrimSpace(capabilities.EraseContractVersion) != "" && !capabilities.Erase {
		return fmt.Errorf("sources.snaplink.capabilities.erase_contract_version requires capabilities.erase")
	}
	return nil
}

func validateErasePathRelationship(path string, enabled bool) error {
	configured := strings.TrimSpace(path) != ""
	if configured && !enabled {
		return fmt.Errorf("sources.snaplink.erase_path requires sources.snaplink.capabilities.erase")
	}
	if enabled && !configured {
		return fmt.Errorf("sources.snaplink.capabilities.erase requires sources.snaplink.erase_path")
	}
	return nil
}

func validateStatusPathRelationship(path string, enabled bool) error {
	configured := strings.TrimSpace(path) != ""
	if configured && !enabled {
		return fmt.Errorf("sources.snaplink.status_path requires sources.snaplink.capabilities.operation_status")
	}
	if enabled && !configured {
		return fmt.Errorf("sources.snaplink.capabilities.operation_status requires sources.snaplink.status_path")
	}
	return nil
}

func validateEraseContract(capabilities Capabilities) error {
	if !capabilities.Erase {
		return nil
	}
	version := strings.TrimSpace(capabilities.EraseContractVersion)
	if version == "" {
		return fmt.Errorf("sources.snaplink.capabilities.erase requires a recognized erase_contract_version")
	}
	if !recognizedEraseContractVersion(version) {
		return fmt.Errorf("sources.snaplink.capabilities.erase_contract_version is not recognized")
	}
	return nil
}

func validateCapabilityEndpoint(cfg connector.HTTPConfig, capabilities Capabilities) error {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint != "" && !capabilities.Summary {
		return fmt.Errorf("sources.snaplink.endpoint requires sources.snaplink.capabilities.summary")
	}
	if endpoint == "" && (capabilities.Summary || capabilities.Erase || capabilities.OperationStatus) {
		return fmt.Errorf("sources.snaplink capabilities require a non-empty sources.snaplink.endpoint")
	}
	return nil
}

func recognizedEraseContractVersion(version string) bool {
	_, ok := eraseContractVersions[strings.TrimSpace(version)]
	return ok
}
