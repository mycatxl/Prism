package platform

import (
	"prism/internal/model"
)

// DecodeQualityPolicy converts a persisted model.QualityPolicy to runtime QualityPolicy.
func DecodeQualityPolicy(mp model.QualityPolicy) QualityPolicy {
	return QualityPolicy{
		ProfileID:                   mp.ProfileID,
		IPTypes:                     mp.IPTypes,
		MinScore:                    mp.MinScore,
		MinConfidence:               mp.MinConfidence,
		MaxAssessmentAgeSeconds:     mp.MaxAssessmentAgeSeconds,
		MaxEgressAgeSeconds:         mp.MaxEgressAgeSeconds,
		UnknownAction:               mp.UnknownAction,
		ConflictAction:              mp.ConflictAction,
		PendingAction:               mp.PendingAction,
		ExcludeHighRisk:             mp.ExcludeHighRisk,
		ExcludeTor:                  mp.ExcludeTor,
	}
}

// EncodeQualityPolicy converts runtime QualityPolicy to persisted model.QualityPolicy.
func EncodeQualityPolicy(qp QualityPolicy) model.QualityPolicy {
	return model.QualityPolicy{
		ProfileID:                   qp.ProfileID,
		IPTypes:                     qp.IPTypes,
		MinScore:                    qp.MinScore,
		MinConfidence:               qp.MinConfidence,
		MaxAssessmentAgeSeconds:     qp.MaxAssessmentAgeSeconds,
		MaxEgressAgeSeconds:         qp.MaxEgressAgeSeconds,
		UnknownAction:               qp.UnknownAction,
		ConflictAction:              qp.ConflictAction,
		PendingAction:               qp.PendingAction,
		ExcludeHighRisk:             qp.ExcludeHighRisk,
		ExcludeTor:                  qp.ExcludeTor,
	}
}
