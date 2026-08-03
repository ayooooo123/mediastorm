package handlers

import (
	"fmt"

	"novastream/config"
	"novastream/models"
	filterutil "novastream/utils/filter"
)

func validateTermList(path string, terms []string) error {
	if err := filterutil.ValidateTerms(terms); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func validateConfigFilterTerms(path string, settings *config.FilterSettings) error {
	if settings == nil {
		return nil
	}
	fields := []struct {
		name  string
		terms []string
	}{
		{"requiredTerms", settings.RequiredTerms},
		{"filterOutTerms", settings.FilterOutTerms},
		{"preferredTerms", settings.PreferredTerms},
		{"nonPreferredTerms", settings.NonPreferredTerms},
		{"downloadPreferredTerms", settings.DownloadPreferredTerms},
	}
	for _, field := range fields {
		if err := validateTermList(path+"."+field.name, field.terms); err != nil {
			return err
		}
	}
	if err := validateConfigFilterTerms(path+".debrid", settings.Debrid); err != nil {
		return err
	}
	return validateConfigFilterTerms(path+".usenet", settings.Usenet)
}

func validateUserFilterTerms(path string, settings *models.FilterSettings) error {
	if settings == nil {
		return nil
	}
	fields := []struct {
		name  string
		terms []string
	}{
		{"requiredTerms", settings.RequiredTerms},
		{"filterOutTerms", settings.FilterOutTerms},
		{"preferredTerms", settings.PreferredTerms},
		{"nonPreferredTerms", settings.NonPreferredTerms},
		{"downloadPreferredTerms", settings.DownloadPreferredTerms},
	}
	for _, field := range fields {
		if err := validateTermList(path+"."+field.name, field.terms); err != nil {
			return err
		}
	}
	if err := validateUserFilterTerms(path+".debrid", settings.Debrid); err != nil {
		return err
	}
	return validateUserFilterTerms(path+".usenet", settings.Usenet)
}

func validateClientFilterTerms(path string, settings *models.ClientFilterSettings) error {
	if settings == nil {
		return nil
	}
	fields := []struct {
		name  string
		terms *[]string
	}{
		{"requiredTerms", settings.RequiredTerms},
		{"filterOutTerms", settings.FilterOutTerms},
		{"preferredTerms", settings.PreferredTerms},
		{"nonPreferredTerms", settings.NonPreferredTerms},
		{"downloadPreferredTerms", settings.DownloadPreferredTerms},
	}
	for _, field := range fields {
		if field.terms != nil {
			if err := validateTermList(path+"."+field.name, *field.terms); err != nil {
				return err
			}
		}
	}
	if err := validateClientFilterTerms(path+".debrid", settings.Debrid); err != nil {
		return err
	}
	return validateClientFilterTerms(path+".usenet", settings.Usenet)
}
