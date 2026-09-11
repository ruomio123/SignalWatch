package operations

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"signalwatch/internal/source"
)

type DependencyCheck func(context.Context) error

type Service struct {
	db            *gorm.DB
	store         SnapshotStore
	mysqlCheck    DependencyCheck
	redisCheck    DependencyCheck
	now           func() time.Time
	dailySyncTime string
}

func NewService(
	db *gorm.DB,
	store SnapshotStore,
	mysqlCheck DependencyCheck,
	redisCheck DependencyCheck,
	now func() time.Time,
	dailySyncTime string,
) (*Service, error) {
	if db == nil || store == nil || mysqlCheck == nil || redisCheck == nil || now == nil {
		return nil, errors.New("invalid operations service configuration")
	}
	if _, err := time.Parse("15:04", dailySyncTime); err != nil {
		return nil, errors.New("invalid operations daily sync time")
	}
	return &Service{db: db, store: store, mysqlCheck: mysqlCheck, redisCheck: redisCheck, now: now, dailySyncTime: dailySyncTime}, nil
}

func (service *Service) Status(ctx context.Context) (StatusResponse, error) {
	now := service.now().UTC()
	result := StatusResponse{
		GeneratedAt: now, Status: "healthy", Workers: []WorkerSnapshot{}, Tasks: []TaskSnapshot{},
		Dependencies: make(map[string]DependencyStatus, 2),
	}
	mysql := checkDependency(ctx, service.mysqlCheck)
	redis := checkDependency(ctx, service.redisCheck)
	result.Dependencies["mysql"], result.Dependencies["redis"] = mysql, redis
	if mysql.Status != "available" || redis.Status != "available" {
		result.Status = "unavailable"
	}
	if mysql.Status == "available" {
		due, dueErr := latestDailyBoundary(now, service.dailySyncTime)
		if dueErr != nil {
			return StatusResponse{}, dueErr
		} else if summary, summaryErr := service.sourceSummary(ctx, due); summaryErr != nil {
			return StatusResponse{}, fmt.Errorf("assemble source checkpoint summary: %w", summaryErr)
		} else {
			result.Sources = summary
		}
	}
	if redis.Status == "available" {
		workers, err := service.store.ListWorkers(ctx, now)
		if err != nil {
			result.Status = "unavailable"
			result.Dependencies["redis"] = DependencyStatus{Status: "unavailable", LatencyMS: redis.LatencyMS}
		} else {
			result.Workers = classifyWorkers(workers, now)
		}
		tasks, err := service.store.ListTasks(ctx, now)
		if err != nil {
			result.Status = "unavailable"
			result.Dependencies["redis"] = DependencyStatus{Status: "unavailable", LatencyMS: redis.LatencyMS}
		} else {
			result.Tasks = tasks
			sort.Slice(result.Tasks, func(i, j int) bool {
				if result.Tasks[i].Task == result.Tasks[j].Task {
					return result.Tasks[i].InstanceID < result.Tasks[j].InstanceID
				}
				return result.Tasks[i].Task < result.Tasks[j].Task
			})
		}
	}
	if result.Status == "healthy" && (isDegraded(result.Workers, result.Tasks) ||
		result.Sources.Stale > 0 || result.Sources.NeverSynced > 0) {
		result.Status = "degraded"
	}
	result.AI = map[string]any{"instances": []TaskSnapshot{}}
	instances := []TaskSnapshot{}
	for _, task := range result.Tasks {
		if task.Task == "ai" {
			instances = append(instances, task)
		}
	}
	result.AI["instances"] = instances
	return result, nil
}

func (service *Service) sourceSummary(ctx context.Context, due time.Time) (SourceSummary, error) {
	type counts struct {
		Enabled     int64 `gorm:"column:enabled"`
		Current     int64 `gorm:"column:current_count"`
		Stale       int64 `gorm:"column:stale"`
		NeverSynced int64 `gorm:"column:never_synced"`
	}
	var row counts
	err := service.db.WithContext(ctx).Table("sources").
		Select(`COUNT(*) AS enabled,
			COALESCE(SUM(CASE WHEN last_successful_sync_at >= ? THEN 1 ELSE 0 END), 0) AS current_count,
			COALESCE(SUM(CASE WHEN last_successful_sync_at < ? THEN 1 ELSE 0 END), 0) AS stale,
			COALESCE(SUM(CASE WHEN last_successful_sync_at IS NULL THEN 1 ELSE 0 END), 0) AS never_synced`, due, due).
		Where("kind = ? AND enabled = ?", source.KindArXiv, true).Scan(&row).Error
	if err != nil {
		return SourceSummary{}, err
	}
	return SourceSummary(row), nil
}

func (service *Service) Sources(ctx context.Context) (SourcesResponse, error) {
	now := service.now().UTC()
	type sourceRow struct {
		ID                   uint64     `gorm:"column:id"`
		SourceKey            string     `gorm:"column:source_key"`
		Kind                 string     `gorm:"column:kind"`
		Name                 string     `gorm:"column:name"`
		Enabled              bool       `gorm:"column:enabled"`
		ConfigJSON           []byte     `gorm:"column:config_json"`
		LastSuccessfulSyncAt *time.Time `gorm:"column:last_successful_sync_at"`
		PaperCount           int64      `gorm:"column:paper_count"`
		LatestPaperAt        *time.Time `gorm:"column:latest_paper_at"`
	}
	var rows []sourceRow
	err := service.db.WithContext(ctx).Table("sources AS s").
		Select(`s.id, s.source_key, s.kind, s.name, s.enabled, s.config_json,
			s.last_successful_sync_at, COUNT(p.id) AS paper_count,
			MAX(p.published_at) AS latest_paper_at`).
		Joins("LEFT JOIN papers AS p ON p.source_id = s.id").
		Where("s.kind = ?", source.KindArXiv).
		Group("s.id, s.source_key, s.kind, s.name, s.enabled, s.config_json, s.last_successful_sync_at").
		Order("s.id ASC").Scan(&rows).Error
	if err != nil {
		return SourcesResponse{}, fmt.Errorf("list operational sources: %w", err)
	}
	due, err := latestDailyBoundary(now, service.dailySyncTime)
	if err != nil {
		return SourcesResponse{}, err
	}
	result := SourcesResponse{GeneratedAt: now, Items: make([]SourceStatus, 0, len(rows))}
	for _, row := range rows {
		stored := source.Source{
			ID: row.ID, SourceKey: row.SourceKey, Kind: row.Kind, Name: row.Name,
			Enabled: row.Enabled, ConfigJSON: row.ConfigJSON, LastSuccessfulSyncAt: row.LastSuccessfulSyncAt,
		}
		public, err := stored.Public()
		if err != nil {
			return SourcesResponse{}, fmt.Errorf("decode source %d configuration: %w", row.ID, err)
		}
		item := SourceStatus{
			ID: row.ID, SourceKey: row.SourceKey, Name: row.Name, Enabled: row.Enabled,
			AllowedCategories: public.AllowedCategories, PaperCount: row.PaperCount,
			LatestPaperAt: utcPointer(row.LatestPaperAt), LastSuccessfulSyncAt: utcPointer(row.LastSuccessfulSyncAt),
			CheckpointState: checkpointState(row.Enabled, row.LastSuccessfulSyncAt, due), OperationalState: "available",
		}
		attempt, attemptErr := service.store.GetSourceAttempt(ctx, row.ID)
		if attemptErr != nil {
			item.OperationalState = "unavailable"
		} else {
			item.LatestAttempt = attempt
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func checkDependency(ctx context.Context, check DependencyCheck) DependencyStatus {
	started := time.Now()
	status := "available"
	if err := check(ctx); err != nil {
		status = "unavailable"
	}
	return DependencyStatus{Status: status, LatencyMS: time.Since(started).Milliseconds()}
}

func classifyWorkers(workers []WorkerSnapshot, now time.Time) []WorkerSnapshot {
	for index := range workers {
		threshold := max(3*time.Duration(workers[index].HeartbeatSeconds)*time.Second, 30*time.Second)
		switch {
		case workers[index].State == "stopped":
			workers[index].OnlineState = "stopped"
		case now.Sub(workers[index].LastHeartbeatAt) <= threshold:
			workers[index].OnlineState = "online"
		default:
			workers[index].OnlineState = "stale"
		}
	}
	sort.Slice(workers, func(i, j int) bool { return workers[i].InstanceID < workers[j].InstanceID })
	return workers
}

func isDegraded(workers []WorkerSnapshot, tasks []TaskSnapshot) bool {
	online := false
	onlineInstances := make(map[string]bool, len(workers))
	for _, worker := range workers {
		online = online || worker.OnlineState == "online"
		if worker.OnlineState == "online" {
			onlineInstances[worker.InstanceID] = true
		}
		if queuePressure(worker.MatcherQueue) || queuePressure(worker.MailQueue) {
			return true
		}
	}
	if !online {
		return true
	}
	taskKinds := make(map[string]bool, 4)
	for _, task := range tasks {
		if !onlineInstances[task.InstanceID] {
			continue
		}
		taskKinds[task.Task] = true
		if task.State == "failed" || task.State == "retry_wait" {
			return true
		}
	}
	for _, required := range []string{"collector", "matcher", "digest", "mail"} {
		if !taskKinds[required] {
			return true
		}
	}
	return false
}

func queuePressure(queue QueueSnapshot) bool {
	return queue.Capacity > 0 && float64(queue.Depth)/float64(queue.Capacity) >= 0.8
}

func checkpointState(enabled bool, checkpoint *time.Time, due time.Time) string {
	if !enabled {
		return "disabled"
	}
	if checkpoint == nil {
		return "never_synced"
	}
	if checkpoint.UTC().Before(due.UTC()) {
		return "stale"
	}
	return "current"
}

func latestDailyBoundary(now time.Time, clock string) (time.Time, error) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.Time{}, err
	}
	parts := strings.Split(clock, ":")
	hour, _ := strconv.Atoi(parts[0])
	minute, _ := strconv.Atoi(parts[1])
	local := now.In(location)
	due := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, location)
	if local.Before(due) {
		due = due.AddDate(0, 0, -1)
	}
	return due.UTC(), nil
}

func utcPointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}
