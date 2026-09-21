package grading

import (
	"errors"
	"fmt"

	"github.com/qoke/toughdecisions/internal/hash"
)

// ErrSameFamily is returned when every candidate grader shares the
// response family and no substitute is available.
var ErrSameFamily = errors.New("grading: no eligible grader")

// ErrSubstituteUnadmitted is returned when routing reaches the substitute
// grader but it is not admitted.
var ErrSubstituteUnadmitted = errors.New("grading: substitute grader not admitted")

// SelectGrader picks the grader for a response per R-12: a grader from the
// same family as the response must never grade it. candidates carries the
// graders for the requested role in preference order; the first grader
// whose family differs from responseFamily is returned. When every
// candidate shares the response family, substitute (role "substitute") is
// returned if non-nil and admitted; otherwise selection is a hard error.
// It never returns a same-family grader.
func SelectGrader(candidates []*Grader, responseFamily string, substitute *Grader) (*Grader, error) {
	for _, g := range candidates {
		if g == nil {
			continue
		}
		if g.Family != responseFamily {
			return g, nil
		}
	}
	if substitute == nil {
		return nil, fmt.Errorf("%w: all candidates share family %q and no substitute configured",
			ErrSameFamily, responseFamily)
	}
	if !substitute.Admitted {
		return nil, fmt.Errorf("%w: %q", ErrSubstituteUnadmitted, substitute.GraderKey)
	}
	if substitute.Family == responseFamily {
		return nil, fmt.Errorf("%w: substitute shares family %q", ErrSameFamily, responseFamily)
	}
	return substitute, nil
}

// GradeCacheKey returns the R-08 grade cache key: sha256 over response_id +
// grader config_hash + rubric_hash.
func GradeCacheKey(responseID, graderConfigHash, rubricHash string) string {
	return hash.SHA256Hex(hash.CanonicalJSON(map[string]any{
		"response_id":        responseID,
		"grader_config_hash": graderConfigHash,
		"rubric_hash":        rubricHash,
	}))
}
