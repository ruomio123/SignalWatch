package user

import (
	"errors"
	"regexp"
	"time"
)

const (
	minItemsPerDigest uint16 = 1
	maxItemsPerDigest uint16 = 50
)

var (
	digestTimePattern = regexp.MustCompile(`^(?:[01][0-9]|2[0-3]):[0-5][0-9]$`)

	ErrEmptyProfileUpdate       = errors.New("profile update is empty")
	ErrInvalidTimezone          = errors.New("invalid timezone")
	ErrInvalidDigestTime        = errors.New("invalid digest time")
	ErrInvalidMaxItemsPerDigest = errors.New("invalid max items per digest")
)

// UpdateProfileInput uses pointers so the service can distinguish an omitted
// field from a supplied zero value.
type UpdateProfileInput struct {
	Timezone          *string
	DigestTime        *string
	MaxItemsPerDigest *uint16
}

// ProfileChanges contains values normalized for database storage.
type ProfileChanges struct {
	Timezone          *string
	DigestTime        *string
	MaxItemsPerDigest *uint16
}

func validateProfileUpdate(input UpdateProfileInput) (ProfileChanges, error) {
	if input.Timezone == nil && input.DigestTime == nil && input.MaxItemsPerDigest == nil {
		return ProfileChanges{}, ErrEmptyProfileUpdate
	}

	changes := ProfileChanges{MaxItemsPerDigest: input.MaxItemsPerDigest}
	if input.Timezone != nil {
		if *input.Timezone == "" || *input.Timezone == "Local" {
			return ProfileChanges{}, ErrInvalidTimezone
		}
		if _, err := time.LoadLocation(*input.Timezone); err != nil {
			return ProfileChanges{}, ErrInvalidTimezone
		}
		changes.Timezone = input.Timezone
	}

	if input.DigestTime != nil {
		if !digestTimePattern.MatchString(*input.DigestTime) {
			return ProfileChanges{}, ErrInvalidDigestTime
		}
		databaseTime := *input.DigestTime + ":00"
		changes.DigestTime = &databaseTime
	}

	if input.MaxItemsPerDigest != nil &&
		(*input.MaxItemsPerDigest < minItemsPerDigest ||
			*input.MaxItemsPerDigest > maxItemsPerDigest) {
		return ProfileChanges{}, ErrInvalidMaxItemsPerDigest
	}

	return changes, nil
}
