package model

import "time"

// BackgroundTaskRecord persists the operator-facing lifecycle of scan,
// organize, and other long-running application tasks.
type BackgroundTaskRecord struct {
	ID         string     `gorm:"primaryKey;size:36" json:"id"`
	Kind       string     `gorm:"index;size:32;not null" json:"kind"`
	Name       string     `gorm:"size:255;not null" json:"name"`
	Status     string     `gorm:"index;size:24;not null" json:"status"`
	Stage      string     `gorm:"size:64" json:"stage"`
	SourcePath string     `gorm:"size:2048" json:"source_path"`
	DestPath   string     `gorm:"size:2048" json:"dest_path"`
	Message    string     `gorm:"type:text" json:"message"`
	Error      string     `gorm:"type:text" json:"error"`
	Details    string     `gorm:"type:text" json:"details"`
	Metrics    string     `gorm:"type:text" json:"metrics"`
	Items      string     `gorm:"type:text" json:"items"`
	StartedAt  time.Time  `gorm:"index" json:"started_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}
