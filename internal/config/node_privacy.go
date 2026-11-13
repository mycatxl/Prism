package config

import "prism/internal/nodeprivacy"

type NodePrivacyConfig = nodeprivacy.Config

const DefaultSelfProbeURL = nodeprivacy.DefaultSelfProbeURL

func ValidateSelfProbeURL(value string) error { return nodeprivacy.ValidateSelfProbeURL(value) }
