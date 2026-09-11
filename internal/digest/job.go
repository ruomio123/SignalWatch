package digest

import (
	"errors"
	"time"
)

func validateJob(job Job) error {
	if job.UserID == 0 || job.SubscriptionID == 0 {
		return errors.New("invalid digest user id")
	}
	if _, err := time.Parse(localDateLayout, job.LocalDate); err != nil {
		return errors.New("invalid digest local date")
	}
	return nil
}
