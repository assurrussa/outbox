package outbox

import (
	"errors"
	"fmt"
	"sort"

	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/types"
)

type (
	SchemaVersion = types.SchemaVersion
	LeaseToken    = types.LeaseToken
)

const (
	DefaultSchemaVersion    SchemaVersion = types.DefaultSchemaVersion
	MaxReservationBatchSize               = 1000
)

var (
	// ErrNoJobs means a repository found no jobs available for claiming.
	// It is the same sentinel as sharederrors.ErrNoJobs for compatibility.
	ErrNoJobs = sharederrors.ErrNoJobs

	ErrReservationBatchSizeUnsupported = errors.New("outbox reservation batch size is unsupported by repository")
	// ErrEmptyReservationBatch means a repository returned no claimed jobs with a nil error.
	// Repositories must return ErrNoJobs when no jobs are available.
	ErrEmptyReservationBatch = errors.New(
		"outbox repository returned an empty reservation batch with a nil error; return ErrNoJobs when no jobs are available",
	)
	ErrUniqueRepositoryNotConfigured = errors.New("outbox unique repository is not configured")
	ErrInvalidSchemaVersion          = errors.New("outbox schema version must be positive")
	ErrLeaseLost                     = errors.New("outbox job lease lost")
	ErrUnsupportedClaim              = errors.New("outbox repository claimed an unsupported capability")
)

// JobCapability identifies a handler and persisted payload schema understood by a worker.
type JobCapability struct {
	Name          string
	SchemaVersion SchemaVersion
}

func (c JobCapability) Validate() error {
	if c.SchemaVersion <= 0 {
		return fmt.Errorf("%w: %d", ErrInvalidSchemaVersion, c.SchemaVersion)
	}

	return nil
}

// VersionedJob opts a handler into an explicit payload schema version.
// Jobs that do not implement this interface use DefaultSchemaVersion.
type VersionedJob interface {
	Job
	SchemaVersion() SchemaVersion
}

func capabilityForJob(job Job) (JobCapability, error) {
	capability := JobCapability{
		Name:          job.Name(),
		SchemaVersion: DefaultSchemaVersion,
	}

	if versioned, ok := job.(VersionedJob); ok {
		capability.SchemaVersion = versioned.SchemaVersion()
	}

	if err := capability.Validate(); err != nil {
		return JobCapability{}, err
	}

	return capability, nil
}

func capabilityForBatchJob(job BatchJob) (JobCapability, error) {
	capability := JobCapability{
		Name:          job.Name(),
		SchemaVersion: DefaultSchemaVersion,
	}
	if versioned, ok := job.(VersionedBatchJob); ok {
		capability.SchemaVersion = versioned.SchemaVersion()
	}
	if err := capability.Validate(); err != nil {
		return JobCapability{}, err
	}

	return capability, nil
}

func normalizeSchemaVersion(version SchemaVersion) SchemaVersion {
	if version <= 0 {
		return DefaultSchemaVersion
	}

	return version
}

func (s *Service) registeredSingleCapabilities() []JobCapability {
	s.mu.RLock()
	capabilities := make([]JobCapability, 0, len(s.jobs))
	for capability := range s.jobs {
		capabilities = append(capabilities, capability)
	}
	s.mu.RUnlock()
	sortCapabilities(capabilities)

	return capabilities
}

func (s *Service) registeredBatchCapabilities() []JobCapability {
	s.mu.RLock()
	capabilities := make([]JobCapability, 0, len(s.batchJobs))
	for capability := range s.batchJobs {
		capabilities = append(capabilities, capability)
	}
	s.mu.RUnlock()
	sortCapabilities(capabilities)

	return capabilities
}

func sortCapabilities(capabilities []JobCapability) {
	sort.Slice(capabilities, func(i, j int) bool {
		if capabilities[i].Name == capabilities[j].Name {
			return capabilities[i].SchemaVersion < capabilities[j].SchemaVersion
		}
		return capabilities[i].Name < capabilities[j].Name
	})
}
