// export_test.go exposes unexported functions for testing from the nomad_test package.
package nomad

import (
	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/prometheus/client_golang/prometheus"
)

// MergeSelectionReason wraps mergeSelectionReason for external test access.
func MergeSelectionReason(existing, incoming SelectionReason) SelectionReason {
	return mergeSelectionReason(existing, incoming)
}

// HasContentDiff wraps hasContentDiff for external test access.
func HasContentDiff(d *nomadapi.JobDiff) bool {
	return hasContentDiff(d)
}

// RedactedFieldsCounter exposes the redaction counter for metric assertions.
func RedactedFieldsCounter(d *Differ) prometheus.Counter {
	return d.redactedFields
}
