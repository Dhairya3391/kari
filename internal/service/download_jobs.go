package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"kari/internal/provider"
)

// DownloadJob stores provider-independent selection data needed to resolve a
// fresh media URL after Kari restarts.
type DownloadJob struct {
	Series  provider.SearchResult `json:"series"`
	Episode provider.Episode      `json:"episode"`
	Mode    provider.ContentType  `json:"mode"`
	// SearchOnResume marks jobs discovered from old partial files that predate
	// persisted provider selection data.
	SearchOnResume bool `json:"search_on_resume,omitempty"`
}

func (s *DownloadService) jobPath() string {
	return filepath.Join(s.downloadDir, ".kari-download.json")
}

// SavePendingJob persists the selected item so a partial download can be
// rediscovered and resolved again after an app restart.
func (s *DownloadService) SavePendingJob(job DownloadJob) error {
	if err := os.MkdirAll(s.downloadDir, 0o755); err != nil {
		return fmt.Errorf("create download directory: %w", err)
	}
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("encode pending download: %w", err)
	}
	tmp := s.jobPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write pending download: %w", err)
	}
	if err := os.Rename(tmp, s.jobPath()); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("save pending download: %w", err)
	}
	return nil
}

// PendingJob returns the interrupted selection saved from the previous run.
func (s *DownloadService) PendingJob() (*DownloadJob, error) {
	if s.downloadDir == "" {
		return nil, nil
	}
	data, err := os.ReadFile(s.jobPath())
	if os.IsNotExist(err) {
		return s.findLegacyPartialJob()
	}
	if err != nil {
		return nil, fmt.Errorf("read pending download: %w", err)
	}
	var job DownloadJob
	if err := json.Unmarshal(data, &job); err != nil {
		return nil, fmt.Errorf("decode pending download: %w", err)
	}
	return &job, nil
}

func (s *DownloadService) findLegacyPartialJob() (*DownloadJob, error) {
	var found string
	err := filepath.WalkDir(s.downloadDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if entry.IsDir() || !isPartialArtifact(entry.Name()) {
			return nil
		}
		found = path
		return filepath.SkipAll
	})
	if err != nil {
		return nil, fmt.Errorf("scan interrupted downloads: %w", err)
	}
	if found == "" {
		return nil, nil
	}

	rel, err := filepath.Rel(s.downloadDir, found)
	if err != nil {
		return nil, fmt.Errorf("locate interrupted download: %w", err)
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	seriesTitle := parts[0]
	if seriesTitle == "." || seriesTitle == "" {
		return nil, nil
	}
	job := DownloadJob{
		Series:         provider.SearchResult{Title: seriesTitle, MediaType: provider.MediaTypeMovie},
		Mode:           provider.ModeMovies,
		SearchOnResume: true,
	}
	if err := s.SavePendingJob(job); err != nil {
		return nil, err
	}
	return &job, nil
}

func isPartialArtifact(name string) bool {
	return strings.HasSuffix(name, ".part") || strings.HasSuffix(name, ".ytdl") ||
		strings.HasSuffix(name, ".aria2") || strings.Contains(name, ".part-Frag")
}

// ClearPendingJob removes the saved selection after completion or deletion.
func (s *DownloadService) ClearPendingJob() error {
	if err := os.Remove(s.jobPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear pending download: %w", err)
	}
	return nil
}
