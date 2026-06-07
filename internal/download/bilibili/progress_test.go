package bilibili

import (
	"testing"

	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

func TestDASHProgressTrackerMonotonic(t *testing.T) {
	properties := newProperties()
	sizeGen := gen.Int64Range(1, 1_000_000_000)

	properties.Property("progress is monotonically increasing", prop.ForAll(
		func(videoSize, audioSize int64) bool {
			progressValues := make([]float64, 0, 25)
			tracker := NewDASHProgressTracker(videoSize, audioSize, func(p float64) {
				progressValues = append(progressValues, p)
			})

			for i := 0; i <= 10; i++ {
				tracker.UpdateVideoProgress(float64(i * 10))
			}
			for i := 0; i <= 10; i++ {
				tracker.UpdateAudioProgress(float64(i * 10))
			}
			for _, progress := range []float64{0, 50, 100} {
				tracker.SetMergeProgress(progress)
			}

			return isMonotonic(progressValues)
		},
		sizeGen,
		sizeGen,
	))

	properties.TestingRun(t)
}
