package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	downloadtask "EasyDownload/internal/download/task"
)

func (ctx *managerExecutionContext) PublishFinal(callContext context.Context, temporaryPath string, draft downloadtask.TaskArtifactDraft) (downloadtask.TaskArtifact, error) {
	if ctx == nil || ctx.dm == nil || ctx.task == nil || ctx.execution == nil {
		return downloadtask.TaskArtifact{}, ErrStaleExecution
	}
	if err := callContext.Err(); err != nil {
		return downloadtask.TaskArtifact{}, err
	}
	temporaryPath, err := filepath.Abs(filepath.Clean(strings.TrimSpace(temporaryPath)))
	if err != nil || temporaryPath == "" {
		return downloadtask.TaskArtifact{}, fmt.Errorf("invalid temporary path: %w", err)
	}
	draft, err = inspectArtifactDraft(temporaryPath, draft)
	if err != nil {
		return downloadtask.TaskArtifact{}, err
	}

	persistenceLocked := ctx.dm.statePath != ""
	if persistenceLocked {
		ctx.dm.persistenceMu.Lock()
	}
	ctx.task.mu.Lock()
	if ctx.task.execution != ctx.execution || ctx.execution.phase != executionRunning || !ctx.execution.mutationOpen {
		ctx.task.mu.Unlock()
		if persistenceLocked {
			ctx.dm.persistenceMu.Unlock()
		}
		return downloadtask.TaskArtifact{}, ErrStaleExecution
	}
	policy := ctx.task.OutputPolicy
	if filepath.VolumeName(temporaryPath) != filepath.VolumeName(policy.PlannedFinalPath) {
		ctx.task.mu.Unlock()
		if persistenceLocked {
			ctx.dm.persistenceMu.Unlock()
		}
		return downloadtask.TaskArtifact{}, errors.New("temporary and final paths must be on the same filesystem")
	}
	ctx.execution.phase = executionPublishing
	ctx.execution.mutationOpen = false
	intent := &downloadtask.PublishIntent{
		Generation:       ctx.execution.generation,
		TemporaryPath:    temporaryPath,
		PlannedFinalPath: policy.PlannedFinalPath,
		Draft:            draft,
	}
	ctx.task.PublishIntent = clonePublishIntent(intent)
	ctx.task.mu.Unlock()

	if err := ctx.dm.persistIfConfiguredWithLock(persistenceLocked); err != nil {
		if persistenceLocked {
			ctx.dm.persistenceMu.Unlock()
		}
		return downloadtask.TaskArtifact{}, fmt.Errorf("persist publish intent: %w", err)
	}
	if persistenceLocked {
		ctx.dm.persistenceMu.Unlock()
	}

	for attempts := 0; attempts < 100; attempts++ {
		outcome, err := publishNoReplace(temporaryPath, intent.PlannedFinalPath)
		if err != nil {
			if !errors.Is(err, ErrOutputExists) {
				return downloadtask.TaskArtifact{}, err
			}
			policy, err = ctx.dm.outputAllocator.Reallocate(ctx.task.ID, policy)
			if err != nil {
				return downloadtask.TaskArtifact{}, err
			}
			intent.PlannedFinalPath = policy.PlannedFinalPath
			persistenceLocked := ctx.dm.statePath != ""
			if persistenceLocked {
				ctx.dm.persistenceMu.Lock()
			}
			ctx.task.mu.Lock()
			if ctx.task.execution != ctx.execution || ctx.execution.phase != executionPublishing {
				ctx.task.mu.Unlock()
				if persistenceLocked {
					ctx.dm.persistenceMu.Unlock()
				}
				return downloadtask.TaskArtifact{}, ErrStaleExecution
			}
			ctx.task.OutputPolicy = policy
			ctx.task.PublishIntent = clonePublishIntent(intent)
			ctx.task.mu.Unlock()
			if err := ctx.dm.persistIfConfiguredWithLock(persistenceLocked); err != nil {
				if persistenceLocked {
					ctx.dm.persistenceMu.Unlock()
				}
				return downloadtask.TaskArtifact{}, fmt.Errorf("persist reallocated publish intent: %w", err)
			}
			if persistenceLocked {
				ctx.dm.persistenceMu.Unlock()
			}
			continue
		}

		intent.Draft = applyPublishWarnings(draft, outcome)
		artifact := artifactFromDraft(intent.PlannedFinalPath, intent.Draft)
		persistenceLocked := ctx.dm.statePath != ""
		if persistenceLocked {
			ctx.dm.persistenceMu.Lock()
		}
		ctx.task.mu.Lock()
		if ctx.task.execution != ctx.execution || ctx.execution.phase != executionPublishing {
			ctx.task.mu.Unlock()
			if persistenceLocked {
				ctx.dm.persistenceMu.Unlock()
			}
			return downloadtask.TaskArtifact{}, ErrStaleExecution
		}
		previousArtifacts := downloadtask.CloneSnapshot(downloadtask.TaskSnapshot{Artifacts: ctx.task.Artifacts}).Artifacts
		previousStatus := ctx.task.Status
		previousProgress := ctx.task.ProgressSummary
		previousCompletedAt := ctx.task.CompletedAt
		ctx.task.Artifacts = upsertArtifact(ctx.task.Artifacts, artifact)
		ctx.task.PublishIntent = nil
		// Publishing the primary final artifact is the durable completion commit.
		// Persisting the artifact while leaving the task "running" creates a crash
		// window in which restart can redownload an already-published file.
		if artifact.Primary {
			ctx.task.Status = StatusCompleted
			ctx.task.ProgressSummary.Percent = 100
			ctx.task.ProgressSummary.CurrentStage = "completed"
			ctx.task.ProgressSummary.StageLabel = "已完成"
			ctx.task.CompletedAt = time.Now().Unix()
		}
		ctx.task.mu.Unlock()
		if err := ctx.dm.persistIfConfiguredWithLock(persistenceLocked); err != nil {
			ctx.task.mu.Lock()
			if ctx.task.execution == ctx.execution {
				ctx.task.Artifacts = previousArtifacts
				ctx.task.PublishIntent = clonePublishIntent(intent)
				ctx.task.Status = previousStatus
				ctx.task.ProgressSummary = previousProgress
				ctx.task.CompletedAt = previousCompletedAt
				ctx.execution.mutationOpen = false
				if ctx.execution.stopRequested {
					ctx.execution.phase = executionStopping
					ctx.execution.cancel()
				} else {
					ctx.execution.phase = executionPublishing
				}
			}
			ctx.task.mu.Unlock()
			if persistenceLocked {
				ctx.dm.persistenceMu.Unlock()
			}
			return downloadtask.TaskArtifact{}, fmt.Errorf("persist published artifact: %w", err)
		}
		ctx.task.mu.Lock()
		if ctx.task.execution != ctx.execution || ctx.execution.phase != executionPublishing {
			ctx.task.mu.Unlock()
			if persistenceLocked {
				ctx.dm.persistenceMu.Unlock()
			}
			return downloadtask.TaskArtifact{}, ErrStaleExecution
		}
		if ctx.execution.stopRequested {
			ctx.execution.phase = executionStopping
			ctx.execution.mutationOpen = false
			ctx.execution.cancel()
		} else if artifact.Primary {
			// Primary publish is the terminal commit. Keep the worker barrier until
			// RunTask returns, but permanently close all mutation sinks so
			// post-publish progress/checkpoints cannot alter completed state.
			ctx.execution.phase = executionFinished
			ctx.execution.mutationOpen = false
		} else {
			ctx.execution.phase = executionRunning
			ctx.execution.mutationOpen = true
		}
		ctx.task.mu.Unlock()
		if persistenceLocked {
			ctx.dm.persistenceMu.Unlock()
		}
		return artifact, nil
	}
	return downloadtask.TaskArtifact{}, errors.New("output path reallocation limit exceeded")
}

func inspectArtifactDraft(path string, draft downloadtask.TaskArtifactDraft) (downloadtask.TaskArtifactDraft, error) {
	expectedHash, err := normalizeSHA256(draft.SHA256)
	if err != nil {
		return draft, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return draft, err
	}
	if info.IsDir() {
		return draft, errors.New("final artifact temporary path is a directory")
	}
	if info.Size() <= 0 {
		return draft, errors.New("final artifact temporary file is empty")
	}
	if draft.Size <= 0 {
		return draft, errors.New("final artifact draft size must be positive")
	}
	if draft.Size != info.Size() {
		return draft, fmt.Errorf("artifact size mismatch: got=%d expected=%d", info.Size(), draft.Size)
	}
	file, err := os.Open(path)
	if err != nil {
		return draft, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return draft, err
	}
	actualHash := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(expectedHash, actualHash) {
		return draft, fmt.Errorf("artifact sha256 mismatch: got=%s expected=%s", actualHash, expectedHash)
	}
	draft.SHA256 = expectedHash
	if draft.ID == "" {
		draft.ID = actualHash[:16]
	}
	return draft, nil
}

func normalizeSHA256(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != sha256.Size*2 {
		return "", fmt.Errorf("artifact sha256 must be %d hexadecimal characters", sha256.Size*2)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return "", errors.New("artifact sha256 is not valid hexadecimal")
	}
	return value, nil
}
