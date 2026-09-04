package user

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestServiceGetProfileUsesAuthenticatedUserID(t *testing.T) {
	want := profileTestUser()
	var gotUserID uint64
	repository := repositoryStub{
		findActiveByID: func(_ context.Context, userID uint64) (User, error) {
			gotUserID = userID
			return want, nil
		},
	}

	got, err := NewService(repository).GetProfile(context.Background(), want.ID)
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if gotUserID != want.ID {
		t.Fatalf("expected repository user ID %d, got %d", want.ID, gotUserID)
	}
	if got != want {
		t.Fatalf("expected profile %+v, got %+v", want, got)
	}
}

func TestServiceUpdateProfileNormalizesAllFields(t *testing.T) {
	timezone := "Asia/Shanghai"
	digestTime := "08:30"
	maxItems := uint16(30)
	want := profileTestUser()
	want.Timezone = timezone
	want.DigestTime = "08:30:00"
	want.MaxItemsPerDigest = maxItems

	var gotUserID uint64
	var gotChanges ProfileChanges
	repository := repositoryStub{
		updateProfile: func(_ context.Context, userID uint64, changes ProfileChanges) (User, error) {
			gotUserID = userID
			gotChanges = changes
			return want, nil
		},
	}

	got, err := NewService(repository).UpdateProfile(
		context.Background(),
		want.ID,
		UpdateProfileInput{
			Timezone:          &timezone,
			DigestTime:        &digestTime,
			MaxItemsPerDigest: &maxItems,
		},
	)
	if err != nil {
		t.Fatalf("update profile: %v", err)
	}
	if gotUserID != want.ID {
		t.Fatalf("expected repository user ID %d, got %d", want.ID, gotUserID)
	}
	if gotChanges.Timezone == nil || *gotChanges.Timezone != timezone {
		t.Fatalf("expected timezone %q, got %+v", timezone, gotChanges.Timezone)
	}
	if gotChanges.DigestTime == nil || *gotChanges.DigestTime != "08:30:00" {
		t.Fatalf("expected database digest time 08:30:00, got %+v", gotChanges.DigestTime)
	}
	if gotChanges.MaxItemsPerDigest == nil || *gotChanges.MaxItemsPerDigest != maxItems {
		t.Fatalf("expected max items %d, got %+v", maxItems, gotChanges.MaxItemsPerDigest)
	}
	if got != want {
		t.Fatalf("expected profile %+v, got %+v", want, got)
	}
}

func TestServiceUpdateProfilePreservesOmittedFields(t *testing.T) {
	timezone := "UTC"
	updateCalls := 0
	repository := repositoryStub{
		updateProfile: func(_ context.Context, _ uint64, changes ProfileChanges) (User, error) {
			updateCalls++
			if changes.Timezone == nil || *changes.Timezone != timezone {
				t.Fatalf("expected timezone %q, got %+v", timezone, changes.Timezone)
			}
			if changes.DigestTime != nil || changes.MaxItemsPerDigest != nil {
				t.Fatalf("expected omitted fields to stay nil, got %+v", changes)
			}
			return profileTestUser(), nil
		},
	}

	if _, err := NewService(repository).UpdateProfile(
		context.Background(),
		42,
		UpdateProfileInput{Timezone: &timezone},
	); err != nil {
		t.Fatalf("update profile: %v", err)
	}
	if updateCalls != 1 {
		t.Fatalf("expected one update call, got %d", updateCalls)
	}
}

func TestServiceUpdateProfileRejectsInvalidInputBeforePersistence(t *testing.T) {
	empty := ""
	local := "Local"
	invalidTimezone := "Asia/NotExist"
	fixedOffset := "+08:00"
	invalidShortTime := "8:30"
	invalidHour := "24:00"
	invalidSeconds := "08:30:00"
	zero := uint16(0)
	fiftyOne := uint16(51)

	tests := []struct {
		name  string
		input UpdateProfileInput
		want  error
	}{
		{name: "empty patch", input: UpdateProfileInput{}, want: ErrEmptyProfileUpdate},
		{name: "empty timezone", input: UpdateProfileInput{Timezone: &empty}, want: ErrInvalidTimezone},
		{name: "local timezone", input: UpdateProfileInput{Timezone: &local}, want: ErrInvalidTimezone},
		{name: "unknown timezone", input: UpdateProfileInput{Timezone: &invalidTimezone}, want: ErrInvalidTimezone},
		{name: "fixed offset timezone", input: UpdateProfileInput{Timezone: &fixedOffset}, want: ErrInvalidTimezone},
		{name: "short digest time", input: UpdateProfileInput{DigestTime: &invalidShortTime}, want: ErrInvalidDigestTime},
		{name: "invalid digest hour", input: UpdateProfileInput{DigestTime: &invalidHour}, want: ErrInvalidDigestTime},
		{name: "digest time with seconds", input: UpdateProfileInput{DigestTime: &invalidSeconds}, want: ErrInvalidDigestTime},
		{name: "zero max items", input: UpdateProfileInput{MaxItemsPerDigest: &zero}, want: ErrInvalidMaxItemsPerDigest},
		{name: "max items above limit", input: UpdateProfileInput{MaxItemsPerDigest: &fiftyOne}, want: ErrInvalidMaxItemsPerDigest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			updateCalls := 0
			repository := repositoryStub{
				updateProfile: func(context.Context, uint64, ProfileChanges) (User, error) {
					updateCalls++
					return User{}, nil
				},
			}

			_, err := NewService(repository).UpdateProfile(context.Background(), 42, test.input)
			if !errors.Is(err, test.want) {
				t.Fatalf("expected %v, got %v", test.want, err)
			}
			if updateCalls != 0 {
				t.Fatalf("expected no repository update, got %d calls", updateCalls)
			}
		})
	}
}

func TestProfileServiceMapsMissingOrInactiveUserToNotFound(t *testing.T) {
	repository := repositoryStub{
		findActiveByID: func(context.Context, uint64) (User, error) {
			return User{}, ErrNotFound
		},
		updateProfile: func(context.Context, uint64, ProfileChanges) (User, error) {
			return User{}, ErrNotFound
		},
	}
	service := NewService(repository)
	timezone := "UTC"

	if _, err := service.GetProfile(context.Background(), 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected get error %v, got %v", ErrNotFound, err)
	}
	if _, err := service.UpdateProfile(
		context.Background(),
		42,
		UpdateProfileInput{Timezone: &timezone},
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected update error %v, got %v", ErrNotFound, err)
	}
}

func TestPublicProfileFormatsDigestTime(t *testing.T) {
	profile := profileTestUser().Public()
	if profile.DigestTime != "08:00" {
		t.Fatalf("expected digest time 08:00, got %q", profile.DigestTime)
	}
}

func profileTestUser() User {
	createdAt := time.Date(2026, time.September, 4, 8, 0, 0, 0, time.UTC)
	return User{
		ID:                42,
		Email:             "alice@example.com",
		PasswordHash:      "$2a$10$PASSWORD_HASH_MUST_NOT_LEAK",
		Timezone:          DefaultTimezone,
		DigestTime:        DefaultDigestTime,
		MaxItemsPerDigest: DefaultMaxItemsPerDigest,
		Status:            StatusActive,
		CreatedAt:         createdAt,
		UpdatedAt:         createdAt,
	}
}
