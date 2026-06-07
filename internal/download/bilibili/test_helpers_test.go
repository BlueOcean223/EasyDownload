package bilibili

import "github.com/leanovate/gopter"

func newProperties() *gopter.Properties {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	return gopter.NewProperties(parameters)
}

func isMonotonic(values []float64) bool {
	for i := 1; i < len(values); i++ {
		if values[i] < values[i-1] {
			return false
		}
	}
	return true
}
